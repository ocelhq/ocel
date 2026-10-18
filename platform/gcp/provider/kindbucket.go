package gcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/storage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"google.golang.org/api/iterator"
)

const reasonUnlocked = "it is open to object ACLs or to allUsers, and what a deploy writes in it is reached by IAM alone"

var bucketKind = kind{
	noun:      "bucket",
	terraform: tfStorageBucket,
	properties: func(region string, target item) map[string]any {
		return map[string]any{"location": region, "storage_class": "STANDARD", "versioning": map[string]any{"enabled": target.Versioned}}
	},
	survey: func(ctx context.Context, b bootstrap, _ environment.Tier, target item) (presence, error) {
		return b.bucketPresence(ctx, target.Name)
	},
	make: func(ctx context.Context, b bootstrap, read survey, target item) error {
		return b.makeBucket(ctx, read, target)
	},
	mend: func(ctx context.Context, b bootstrap, _ survey, target item) error {
		return b.lockBucket(ctx, target.Name)
	},
	removal: func(read survey, target item) removal {
		taking := deleting(read, target)
		if target.Name == read.Names.Bucket(read.Tier) {
			taking.item.Slow = true
		}
		return taking
	},
	take: func(ctx context.Context, b bootstrap, _ survey, target item) (time.Duration, error) {
		return 0, b.takeBucket(ctx, target.Name)
	},
}

func (b bootstrap) makeBucket(ctx context.Context, read survey, target item) error {
	client, err := b.clients.Storage()
	if err != nil {
		return err
	}
	if err := client.Bucket(target.Name).Create(ctx, read.Project, bucketAttrs(read.Region, target)); err != nil && !gcloud.Taken(err) {
		return fmt.Errorf("create the %s bucket: %w", target.Name, err)
	}
	return nil
}

func bucketAttrs(region string, target item) *storage.BucketAttrs {
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

func (b bootstrap) lockBucket(ctx context.Context, name string) error {
	client, err := b.clients.Storage()
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

func (b bootstrap) takeBucket(ctx context.Context, name string) error {
	client, err := b.clients.Storage()
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

func (b bootstrap) bucketPresence(ctx context.Context, name string) (presence, error) {
	client, err := b.clients.Storage()
	if err != nil {
		return presence{}, err
	}
	attrs, err := client.Bucket(name).Attrs(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
			return presence{}, nil
		}
		return presence{}, fmt.Errorf("read the %s bucket: %w", name, err)
	}
	return bucketPresenceOf(attrs, b.clients.Emulated()), nil
}

func bucketPresenceOf(attrs *storage.BucketAttrs, emulated bool) presence {
	if !emulated && !locked(attrs) {
		return presence{present: true, mends: reasonUnlocked}
	}
	return presence{present: true}
}
