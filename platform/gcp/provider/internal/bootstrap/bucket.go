package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/storage"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"google.golang.org/api/iterator"
)

const (
	NoncurrentStateLifetimeDays = 7
)

const (
	reasonUnlocked = "it is open to object ACLs or to allUsers, and what a deploy writes in it is reached by IAM alone"
	reasonUnaged   = "its versioning or the lifecycle that deletes what it no longer needs is not the one this bootstrap sets"
)

var bucketKind = KindOps{
	noun:      "bucket",
	terraform: cost.StorageBucketType,
	properties: func(region string, target Resource) map[string]any {
		return map[string]any{"location": region, "storage_class": "STANDARD", "versioning": map[string]any{"enabled": versioned(target.Class)}}
	},
	survey: func(ctx context.Context, b Engine, read Survey, target Resource) (Presence, error) {
		attrs, err := b.readBucket(ctx, read.Buckets[target.Class])
		if err != nil || attrs == nil {
			return Presence{}, err
		}
		return BucketPresenceOf(attrs, target.Class, b.Clients.Emulated()), nil
	},
	Make: func(ctx context.Context, b Engine, read Survey, target Resource) error {
		_, err := b.makeBucket(ctx, read, target)
		return err
	},
	mend: func(ctx context.Context, b Engine, read Survey, target Resource) error {
		return b.mendBucket(ctx, read, target)
	},
	removal: func(read Survey, target Resource) Removal {
		taking := deleting(read, target)
		if target.Class == naming.StorageEdge {
			taking.Resource.Slow = true
		}
		return taking
	},
	Take: func(ctx context.Context, b Engine, read Survey, target Resource) (time.Duration, error) {
		bucket := read.Buckets[target.Class]
		if bucket == "" {
			return 0, nil
		}
		if err := b.takeBucket(ctx, bucket); err != nil {
			return 0, err
		}
		return 0, b.Clients.ForgetTierBucket(ctx, read.Tier, target.Class, bucket)
	},
}

func versioned(class naming.StorageClass) bool { return class == naming.StorageState }

func lifecycleOf(class naming.StorageClass) storage.Lifecycle {
	deleted := storage.LifecycleAction{Type: storage.DeleteAction}
	switch class {
	case naming.StorageState:
		return storage.Lifecycle{Rules: []storage.LifecycleRule{{Action: deleted, Condition: storage.LifecycleCondition{DaysSinceNoncurrentTime: NoncurrentStateLifetimeDays}}}}
	case naming.StorageCache:
		return storage.Lifecycle{Rules: []storage.LifecycleRule{{Action: deleted, Condition: storage.LifecycleCondition{
			AgeInDays:     naming.CacheObjectLifetimeDays,
			MatchesPrefix: expiringPrefixes(),
		}}}}
	}
	return storage.Lifecycle{}
}

func expiringPrefixes() []string {
	prefixes := make([]string, len(naming.ExpiringObjectKinds))
	for i, kind := range naming.ExpiringObjectKinds {
		prefixes[i] = string(kind) + "/"
	}
	return prefixes
}

func (b Engine) readBucket(ctx context.Context, bucket string) (*storage.BucketAttrs, error) {
	if bucket == "" {
		return nil, nil
	}
	client, err := b.Clients.Storage()
	if err != nil {
		return nil, err
	}
	attrs, err := client.Bucket(bucket).Attrs(ctx)
	if errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the %s bucket: %w", bucket, err)
	}
	return attrs, nil
}

func (b Engine) makeBucket(ctx context.Context, read Survey, target Resource) (string, error) {
	client, err := b.Clients.Storage()
	if err != nil {
		return "", err
	}
	var minted string
	name, err := b.Clients.ClaimTierBucket(ctx, read.Tier, target.Class, func() string {
		minted = read.Names.TierBucket(read.Tier, target.Class, naming.NewRandomTag())
		return minted
	})
	if err != nil {
		return "", err
	}
	err = client.Bucket(name).Create(ctx, read.Project, BucketAttrs(read.Region, target))
	if err == nil {
		return name, nil
	}
	if !gcloud.Taken(err) {
		return "", fmt.Errorf("create the %s bucket: %w", name, err)
	}
	attrs, err := b.readBucket(ctx, name)
	if err != nil && !gcloud.Forbidden(err) {
		return "", err
	}
	if attrs != nil {
		return name, nil
	}
	seenFree := name == minted || name == read.Buckets[target.Class]
	if err != nil && !seenFree {
		return "", refusal.Refuse(refusal.CodeDenied,
			"the %s bucket of the %s tier is recorded as %s, and Google Cloud answers that it exists but these credentials cannot read it.\n"+
				"Check that they hold storage.buckets.get on %s in project %s, then bootstrap again: the name stays recorded, since forgetting it would leave what the bucket holds behind",
			target.Class, read.Tier, name, name, read.Project)
	}
	if err := b.Clients.ForgetTierBucket(ctx, read.Tier, target.Class, name); err != nil {
		return "", err
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"the %s bucket of the %s tier would be %s, and another Google Cloud project already holds that name: bucket names are global.\n"+
			"Bootstrap again, which names the bucket with a new random tag", target.Class, read.Tier, name)
}

func BucketAttrs(region string, target Resource) *storage.BucketAttrs {
	return &storage.BucketAttrs{
		Location:                 region,
		VersioningEnabled:        versioned(target.Class),
		Lifecycle:                lifecycleOf(target.Class),
		UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
		PublicAccessPrevention:   storage.PublicAccessPreventionEnforced,
	}
}

func locked(attrs *storage.BucketAttrs) bool {
	return attrs.UniformBucketLevelAccess.Enabled && attrs.PublicAccessPrevention == storage.PublicAccessPreventionEnforced
}

func aged(attrs *storage.BucketAttrs, class naming.StorageClass) bool {
	return attrs.VersioningEnabled == versioned(class) && slices.EqualFunc(attrs.Lifecycle.Rules, lifecycleOf(class).Rules, sameRule)
}

func sameRule(a, b storage.LifecycleRule) bool {
	return a.Action.Type == b.Action.Type &&
		a.Condition.AgeInDays == b.Condition.AgeInDays &&
		a.Condition.DaysSinceNoncurrentTime == b.Condition.DaysSinceNoncurrentTime &&
		slices.Equal(a.Condition.MatchesPrefix, b.Condition.MatchesPrefix)
}

func (b Engine) mendBucket(ctx context.Context, read Survey, target Resource) error {
	attrs, err := b.readBucket(ctx, read.Buckets[target.Class])
	if err != nil {
		return err
	}
	if attrs == nil {
		_, err := b.makeBucket(ctx, read, target)
		return err
	}
	client, err := b.Clients.Storage()
	if err != nil {
		return err
	}
	lifecycle := lifecycleOf(target.Class)
	if _, err := client.Bucket(attrs.Name).Update(ctx, storage.BucketAttrsToUpdate{
		UniformBucketLevelAccess: &storage.UniformBucketLevelAccess{Enabled: true},
		PublicAccessPrevention:   storage.PublicAccessPreventionEnforced,
		VersioningEnabled:        versioned(target.Class),
		Lifecycle:                &lifecycle,
	}); err != nil {
		return fmt.Errorf("restrict the %s bucket to IAM alone and set what it deletes: %w", attrs.Name, err)
	}
	return nil
}

func (b Engine) takeBucket(ctx context.Context, name string) error {
	client, err := b.Clients.Storage()
	if err != nil {
		return err
	}
	bucket := client.Bucket(name)
	objects := bucket.Objects(ctx, &storage.Query{Versions: true})
	for {
		attrs, err := objects.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			if errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
				return nil
			}
			return fmt.Errorf("list what %s contains: %w", name, err)
		}
		object := bucket.Object(attrs.Name)
		if attrs.Generation != 0 {
			object = object.Generation(attrs.Generation)
		}
		if err := googleretry.Done(ctx, func() error { return object.Delete(ctx) }); err != nil &&
			!errors.Is(err, storage.ErrObjectNotExist) && !gcloud.Absent(err) {
			return fmt.Errorf("empty %s: %w", name, err)
		}
	}
	if err := googleretry.Done(ctx, func() error { return bucket.Delete(ctx) }); err != nil &&
		!errors.Is(err, storage.ErrBucketNotExist) && !gcloud.Absent(err) {
		return fmt.Errorf("delete the %s bucket: %w", name, err)
	}
	return nil
}

func BucketPresenceOf(attrs *storage.BucketAttrs, class naming.StorageClass, emulated bool) Presence {
	if emulated {
		return Presence{Present: true}
	}
	if !locked(attrs) {
		return Presence{Present: true, Mends: reasonUnlocked}
	}
	if !aged(attrs, class) {
		return Presence{Present: true, Mends: reasonUnaged}
	}
	return Presence{Present: true}
}
