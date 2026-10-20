package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"cloud.google.com/go/storage"
	"github.com/ocelhq/ocel/pkg/environment"
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
	reasonUnlocked     = "it is open to object ACLs or to allUsers, and what a deploy writes in it is reached by IAM alone"
	reasonUnaged       = "its versioning or the lifecycle that deletes what it no longer needs is not the one this bootstrap sets"
	reasonBucketRacing = "another bootstrap of the %s tier made the %s bucket %s while this one made %s, and a tier keeps its %s objects in one bucket.\n" +
		"Wait for that run to finish, then try again"
)

var bucketKind = KindOps{
	noun:      "bucket",
	terraform: cost.StorageBucketType,
	properties: func(region string, target Resource) map[string]any {
		return map[string]any{"location": region, "storage_class": "STANDARD", "versioning": map[string]any{"enabled": versioned(target.Class)}}
	},
	survey: func(ctx context.Context, b Engine, tier environment.Tier, target Resource) (Presence, error) {
		attrs, err := b.Clients.FindTierBucket(ctx, tier, target.Class)
		if err != nil || attrs == nil {
			return Presence{}, err
		}
		return BucketPresenceOf(attrs, target.Class, b.Clients.Emulated()), nil
	},
	Make: func(ctx context.Context, b Engine, read Survey, target Resource) error {
		return b.makeBucket(ctx, read, target)
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
		return 0, b.takeBuckets(ctx, read.Tier, target.Class)
	},
}

func versioned(class naming.StorageClass) bool { return class == naming.StorageState }

func lifecycleOf(class naming.StorageClass) storage.Lifecycle {
	deleted := storage.LifecycleAction{Type: storage.DeleteAction}
	switch class {
	case naming.StorageState:
		return storage.Lifecycle{Rules: []storage.LifecycleRule{{Action: deleted, Condition: storage.LifecycleCondition{DaysSinceNoncurrentTime: NoncurrentStateLifetimeDays}}}}
	case naming.StorageCache:
		return storage.Lifecycle{Rules: []storage.LifecycleRule{{Action: deleted, Condition: storage.LifecycleCondition{AgeInDays: naming.CacheObjectLifetimeDays}}}}
	}
	return storage.Lifecycle{}
}

func (b Engine) makeBucket(ctx context.Context, read Survey, target Resource) error {
	client, err := b.Clients.Storage()
	if err != nil {
		return err
	}
	name := read.Names.TierBucket(read.Tier, target.Class, naming.NewRandomTag())
	if err := client.Bucket(name).Create(ctx, read.Project, BucketAttrs(read.Region, target)); err != nil {
		if gcloud.Taken(err) {
			return refusal.Refuse(refusal.CodeInvalid,
				"the %s bucket of the %s tier would be %s, and another Google Cloud project already holds that name: bucket names are global.\n"+
					"Bootstrap again, which names the bucket with a new random tag", target.Class, read.Tier, name)
		}
		return fmt.Errorf("create the %s bucket: %w", name, err)
	}
	return b.refuseRacingBucket(ctx, read, target, name)
}

func (b Engine) refuseRacingBucket(ctx context.Context, read Survey, target Resource, made string) error {
	matching, err := b.Clients.ListTierBucketsOf(ctx, read.Tier, target.Class)
	if err != nil {
		return err
	}
	for _, attrs := range matching {
		if attrs.Name == made {
			continue
		}
		if err := b.takeBucket(ctx, made); err != nil {
			return err
		}
		return refusal.Refuse(refusal.CodeBusy, reasonBucketRacing, read.Tier, target.Class, attrs.Name, made, target.Class)
	}
	return nil
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
		a.Condition.DaysSinceNoncurrentTime == b.Condition.DaysSinceNoncurrentTime
}

func (b Engine) mendBucket(ctx context.Context, read Survey, target Resource) error {
	attrs, err := b.Clients.FindTierBucket(ctx, read.Tier, target.Class)
	if err != nil {
		return err
	}
	if attrs == nil {
		return b.makeBucket(ctx, read, target)
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

func (b Engine) takeBuckets(ctx context.Context, tier environment.Tier, class naming.StorageClass) error {
	matching, err := b.Clients.ListTierBucketsOf(ctx, tier, class)
	if err != nil {
		return err
	}
	for _, attrs := range matching {
		if err := b.takeBucket(ctx, attrs.Name); err != nil {
			return err
		}
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
