package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/storage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/cost"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"google.golang.org/api/iterator"
)

const reasonUnlocked = "it is open to object ACLs or to allUsers, and what a deploy writes in it is reached by IAM alone"

var bucketKind = KindOps{
	noun:      "bucket",
	terraform: cost.StorageBucketType,
	properties: func(region string, target Resource) map[string]any {
		return map[string]any{"location": region, "storage_class": "STANDARD", "versioning": map[string]any{"enabled": target.Versioned}}
	},
	survey: func(ctx context.Context, b Engine, _ environment.Tier, target Resource) (Presence, error) {
		return b.bucketPresence(ctx, target.Name)
	},
	Make: func(ctx context.Context, b Engine, read Survey, target Resource) error {
		return b.makeBucket(ctx, read, target)
	},
	mend: func(ctx context.Context, b Engine, _ Survey, target Resource) error {
		return b.lockBucket(ctx, target.Name)
	},
	removal: func(read Survey, target Resource) Removal {
		taking := deleting(read, target)
		if target.Name == read.Names.Bucket(read.Tier) {
			taking.Resource.Slow = true
		}
		return taking
	},
	Take: func(ctx context.Context, b Engine, _ Survey, target Resource) (time.Duration, error) {
		return 0, b.takeBucket(ctx, target.Name)
	},
}

func (b Engine) makeBucket(ctx context.Context, read Survey, target Resource) error {
	client, err := b.Clients.Storage()
	if err != nil {
		return err
	}
	if err := client.Bucket(target.Name).Create(ctx, read.Project, BucketAttrs(read.Region, target)); err != nil && !gcloud.Taken(err) {
		return fmt.Errorf("create the %s bucket: %w", target.Name, err)
	}
	return nil
}

func BucketAttrs(region string, target Resource) *storage.BucketAttrs {
	return &storage.BucketAttrs{
		Location:                 region,
		VersioningEnabled:        target.Versioned,
		UniformBucketLevelAccess: storage.UniformBucketLevelAccess{Enabled: true},
		PublicAccessPrevention:   storage.PublicAccessPreventionEnforced,
	}
}

func locked(attrs *storage.BucketAttrs) bool {
	return attrs.UniformBucketLevelAccess.Enabled && attrs.PublicAccessPrevention == storage.PublicAccessPreventionEnforced
}

func (b Engine) lockBucket(ctx context.Context, name string) error {
	client, err := b.Clients.Storage()
	if err != nil {
		return err
	}
	if _, err := client.Bucket(name).Update(ctx, storage.BucketAttrsToUpdate{
		UniformBucketLevelAccess: &storage.UniformBucketLevelAccess{Enabled: true},
		PublicAccessPrevention:   storage.PublicAccessPreventionEnforced,
	}); err != nil {
		return fmt.Errorf("restrict the %s bucket to IAM alone: %w", name, err)
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

func (b Engine) bucketPresence(ctx context.Context, name string) (Presence, error) {
	client, err := b.Clients.Storage()
	if err != nil {
		return Presence{}, err
	}
	attrs, err := client.Bucket(name).Attrs(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
			return Presence{}, nil
		}
		return Presence{}, fmt.Errorf("read the %s bucket: %w", name, err)
	}
	return BucketPresenceOf(attrs, b.Clients.Emulated()), nil
}

func BucketPresenceOf(attrs *storage.BucketAttrs, emulated bool) Presence {
	if !emulated && !locked(attrs) {
		return Presence{Present: true, Mends: reasonUnlocked}
	}
	return Presence{Present: true}
}
