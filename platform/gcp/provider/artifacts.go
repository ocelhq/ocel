package gcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

var artifactStores = []string{provider.StoreFunctions, provider.StoreAssets, provider.StoreCache}

type artifacts struct {
	p *Provider
}

func (a artifacts) bucket(ctx context.Context, tier environment.Tier) (*storage.BucketHandle, error) {
	if tier == "" {
		return nil, ports.Tierless("an artifact")
	}
	clients, err := a.p.openClients(ctx)
	if err != nil {
		return nil, err
	}
	client, err := clients.Storage()
	if err != nil {
		return nil, err
	}
	return client.Bucket(clients.Bucket(tier)), nil
}

func objectName(ref provider.ArtifactRef) (string, error) {
	if ref.Bucket == provider.StoreCache {
		return cacheObjectName(ref.Key), nil
	}
	for _, store := range artifactStores {
		if ref.Bucket == store {
			return store + "/" + ref.Key, nil
		}
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"this provider keeps no %q store; it keeps %q, %q and %q",
		ref.Bucket, provider.StoreFunctions, provider.StoreAssets, provider.StoreCache)
}

func cacheObjectName(key string) string {
	segments := strings.SplitN(key, "/", 4)
	if len(segments) < 3 {
		return provider.StoreCache + "/" + key
	}
	env, project, app := segments[0], segments[1], segments[2]
	name := provider.StoreCache + "/" + project + "/" + app + "/" + env
	if len(segments) == 4 {
		name += "/" + segments[3]
	}
	return name
}

func cacheSweep(prefix string) (list string, keeps func(name string) bool, err error) {
	keepAll := func(string) bool { return true }
	segments := strings.Split(strings.TrimSuffix(prefix, "/"), "/")
	closed := strings.HasSuffix(prefix, "/")
	switch {
	case len(segments) >= 4, len(segments) == 3 && closed:
		return cacheObjectName(prefix), keepAll, nil
	case len(segments) == 2 && closed:
		env, project := segments[0], segments[1]
		return provider.StoreCache + "/" + project + "/", func(name string) bool {
			parts := strings.Split(name, "/")
			return len(parts) > 3 && parts[3] == env
		}, nil
	}
	return "", nil, refusal.Refuse(refusal.CodeInvalid,
		"the cache store keeps its objects by project, then app, then environment, and %q names no whole project and environment to sweep", prefix)
}

func (a artifacts) object(ctx context.Context, ref provider.ArtifactRef) (*storage.ObjectHandle, error) {
	name, err := objectName(ref)
	if err != nil {
		return nil, err
	}
	bucket, err := a.bucket(ctx, ref.Tier)
	if err != nil {
		return nil, err
	}
	return bucket.Object(name), nil
}

func (a artifacts) Put(ctx context.Context, ref provider.ArtifactRef, body io.Reader) error {
	object, err := a.object(ctx, ref)
	if err != nil {
		return err
	}
	writer := object.NewWriter(ctx)
	if _, err := io.Copy(writer, body); err != nil {
		writer.Close()
		return a.storeless(ctx, ref.Tier, fmt.Errorf("upload %s: %w", object.ObjectName(), err))
	}
	if err := writer.Close(); err != nil {
		return a.storeless(ctx, ref.Tier, fmt.Errorf("upload %s: %w", object.ObjectName(), err))
	}
	return nil
}

func (a artifacts) Has(ctx context.Context, ref provider.ArtifactRef) (bool, error) {
	object, err := a.object(ctx, ref)
	if err != nil {
		return false, err
	}
	if _, err := object.Attrs(ctx); err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return false, nil
		}
		return false, a.storeless(ctx, ref.Tier, fmt.Errorf("look for %s: %w", object.ObjectName(), err))
	}
	return true, nil
}

func (a artifacts) Open(ctx context.Context, ref provider.ArtifactRef) (io.ReadCloser, error) {
	object, err := a.object(ctx, ref)
	if err != nil {
		return nil, err
	}
	reader, err := object.NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, refusal.Refuse(refusal.CodeInvalid, "no artifact at %s", object.ObjectName())
		}
		return nil, a.storeless(ctx, ref.Tier, fmt.Errorf("read %s: %w", object.ObjectName(), err))
	}
	return reader, nil
}

func (a artifacts) RemovePrefix(ctx context.Context, tier environment.Tier, prefix string, progress progress.Log) error {
	if prefix == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"an empty prefix names every artifact this project keeps")
	}
	cacheList, cacheKeeps, err := cacheSweep(prefix)
	if err != nil {
		return err
	}
	bucket, err := a.bucket(ctx, tier)
	if err != nil {
		return err
	}
	var errs []error
	for _, store := range artifactStores {
		list, keeps := store+"/"+prefix, func(string) bool { return true }
		if store == provider.StoreCache {
			list, keeps = cacheList, cacheKeeps
		}
		if err := sweep(ctx, bucket, list, keeps); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	ensureProgress(progress).Say("Removed the " + string(tier) + " artifacts under " + prefix + " from bucket " + bucket.BucketName())
	return nil
}

func sweep(ctx context.Context, bucket *storage.BucketHandle, prefix string, keeps func(name string) bool) error {
	objects := bucket.Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := objects.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			if errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
				return nil
			}
			return fmt.Errorf("list %s: %w", prefix, err)
		}
		if !keeps(attrs.Name) {
			continue
		}
		if err := bucket.Object(attrs.Name).Delete(ctx); err != nil &&
			!errors.Is(err, storage.ErrObjectNotExist) && !errors.Is(err, storage.ErrBucketNotExist) {
			return fmt.Errorf("delete %s: %w", attrs.Name, err)
		}
	}
}

func absent(err error) bool {
	var answered *googleapi.Error
	return errors.As(err, &answered) && answered.Code == http.StatusNotFound
}

func (a artifacts) storeless(ctx context.Context, tier environment.Tier, err error) error {
	names, named := a.p.Names(ctx)
	if named == nil && (errors.Is(err, storage.ErrBucketNotExist) || absent(err)) {
		return refusal.Refuse(refusal.CodeNotReady,
			"this project has no %s bucket, so it has no %s artifact store yet.\nRun `%s` to create it, then try again",
			names.Bucket(tier), tier, provider.BootstrapCommand(tier))
	}
	return err
}
