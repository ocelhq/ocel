package assets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type Store struct {
	OpenClients      func(context.Context) (*gcloud.Clients, error)
	RemoveTagRecords func(ctx context.Context, tier environment.Tier, scope naming.StorageScope, progress progress.Log) error
}

func (a Store) bucket(ctx context.Context, tier environment.Tier, kind naming.ObjectKind) (*storage.BucketHandle, error) {
	if tier == "" {
		return nil, ports.Tierless("an artifact")
	}
	clients, err := a.OpenClients(ctx)
	if err != nil {
		return nil, err
	}
	buckets, err := clients.TierBuckets(ctx, tier)
	if err != nil {
		return nil, err
	}
	name := buckets.Of(kind.Class())
	if name == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"GCP keeps no %s objects: Cloud Run runs every function from its image, so nothing deploys %s/ keys here", kind.Class(), kind)
	}
	client, err := clients.Storage()
	if err != nil {
		return nil, err
	}
	return client.Bucket(name), nil
}

func (a Store) object(ctx context.Context, ref provider.ArtifactRef) (*storage.ObjectHandle, error) {
	kind, err := naming.ParseObjectKind(ref.Key)
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeInvalid, "%s", err.Error())
	}
	bucket, err := a.bucket(ctx, ref.Tier, kind)
	if err != nil {
		return nil, err
	}
	return bucket.Object(ref.Key), nil
}

func (a Store) Put(ctx context.Context, ref provider.ArtifactRef, body io.Reader) error {
	object, err := a.object(ctx, ref)
	if err != nil {
		return err
	}
	writer := object.NewWriter(ctx)
	if _, err := io.Copy(writer, body); err != nil {
		writer.Close()
		return fmt.Errorf("upload %s: %w", object.ObjectName(), err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("upload %s: %w", object.ObjectName(), err)
	}
	return nil
}

func (a Store) Has(ctx context.Context, ref provider.ArtifactRef) (bool, error) {
	object, err := a.object(ctx, ref)
	if err != nil {
		return false, err
	}
	if _, err := object.Attrs(ctx); err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("look for %s: %w", object.ObjectName(), err)
	}
	return true, nil
}

func (a Store) Open(ctx context.Context, ref provider.ArtifactRef) (io.ReadCloser, error) {
	object, err := a.object(ctx, ref)
	if err != nil {
		return nil, err
	}
	reader, err := object.NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, refusal.Refuse(refusal.CodeInvalid, "no artifact at %s", object.ObjectName())
		}
		return nil, fmt.Errorf("read %s: %w", object.ObjectName(), err)
	}
	return reader, nil
}

func (a Store) RemoveObjects(ctx context.Context, tier environment.Tier, scope naming.StorageScope, kinds []naming.ObjectKind, progress progress.Log) error {
	prefixes := make([]string, len(kinds))
	for i, kind := range kinds {
		prefix, err := scope.Prefix(kind)
		if err != nil {
			return err
		}
		prefixes[i] = prefix
	}
	if tier == "" {
		return ports.Tierless("an artifact")
	}
	clients, err := a.OpenClients(ctx)
	if err != nil {
		return err
	}
	buckets, err := clients.TierBuckets(ctx, tier)
	if err != nil {
		return err
	}
	client, err := clients.Storage()
	if err != nil {
		return err
	}
	var errs []error
	var swept []string
	for i, kind := range kinds {
		name := buckets.Of(kind.Class())
		if name == "" {
			continue
		}
		if err := sweep(ctx, client.Bucket(name), prefixes[i]); err != nil {
			errs = append(errs, err)
			continue
		}
		swept = append(swept, "gs://"+name+"/"+prefixes[i])
	}
	if slices.Contains(kinds, naming.ObjectPages) {
		if err := a.RemoveTagRecords(ctx, tier, scope, progress); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say("Removed the " + string(tier) + " objects under " + strings.Join(swept, ", "))
	return nil
}

func sweep(ctx context.Context, bucket *storage.BucketHandle, prefix string) error {
	objects := bucket.Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := objects.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			if errors.Is(err, storage.ErrBucketNotExist) || gcloud.Absent(err) {
				return nil
			}
			return fmt.Errorf("list %s: %w", prefix, err)
		}
		if err := bucket.Object(attrs.Name).Delete(ctx); err != nil &&
			!errors.Is(err, storage.ErrObjectNotExist) && !errors.Is(err, storage.ErrBucketNotExist) {
			return fmt.Errorf("delete %s: %w", attrs.Name, err)
		}
	}
}
