package gcp

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
)

const copiedFromKey = "ocel-copied-from"

func (p *Provider) CopyImmutableFiles(ctx context.Context, tier environment.Tier, from, to alb.StaticRoutes) error {
	objects := artifacts{p}
	bucket, err := objects.bucket(ctx, tier)
	if err != nil {
		return err
	}
	source, target := strings.TrimPrefix(from.Objects, "/"), strings.TrimPrefix(to.Objects, "/")
	for _, prefix := range from.Immutable {
		own, err := listImmutableFiles(ctx, bucket, source+prefix, true)
		if err != nil {
			return objects.storeless(ctx, tier, err)
		}
		held, err := listImmutableFiles(ctx, bucket, target+prefix, false)
		if err != nil {
			return objects.storeless(ctx, tier, err)
		}
		group, ctx := errgroup.WithContext(ctx)
		group.SetLimit(functioncode.UploadConcurrency)
		for _, rel := range slices.Sorted(maps.Keys(own)) {
			if _, present := held[rel]; present || revalidated(from.Revalidate, prefix+rel) {
				continue
			}
			group.Go(func() error {
				defer functioncode.TakeUploadSlot()()
				return copyImmutableFile(ctx, bucket, source+prefix+rel, target+prefix+rel, own[rel], from.Objects)
			})
		}
		if err := group.Wait(); err != nil {
			return objects.storeless(ctx, tier, err)
		}
	}
	return nil
}

func listImmutableFiles(ctx context.Context, bucket *storage.BucketHandle, prefix string, ownOnly bool) (map[string]*storage.ObjectAttrs, error) {
	query := &storage.Query{Prefix: prefix}
	if err := query.SetAttrSelection([]string{"Name", "ContentType", "CacheControl", "Metadata"}); err != nil {
		return nil, err
	}
	files := map[string]*storage.ObjectAttrs{}
	listed := bucket.Objects(ctx, query)
	for {
		attrs, err := listed.Next()
		if errors.Is(err, iterator.Done) {
			return files, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list the immutable files under %s: %w", prefix, err)
		}
		if _, copied := attrs.Metadata[copiedFromKey]; ownOnly && copied {
			continue
		}
		files[strings.TrimPrefix(attrs.Name, prefix)] = attrs
	}
}

func revalidated(prefixes []string, path string) bool {
	return slices.ContainsFunc(prefixes, func(prefix string) bool { return strings.HasPrefix(path, prefix) })
}

func copyImmutableFile(ctx context.Context, bucket *storage.BucketHandle, src, dst string, attrs *storage.ObjectAttrs, from string) error {
	copier := bucket.Object(dst).If(storage.Conditions{DoesNotExist: true}).CopierFrom(bucket.Object(src))
	copier.ContentType = attrs.ContentType
	copier.CacheControl = attrs.CacheControl
	copier.Metadata = map[string]string{copiedFromKey: from}
	_, err := copier.Run(ctx)
	var failure *googleapi.Error
	if err == nil || errors.As(err, &failure) && failure.Code == http.StatusPreconditionFailed {
		return nil
	}
	return fmt.Errorf("copy %s to %s: %w", src, dst, err)
}
