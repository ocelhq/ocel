package gcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

var artifactStores = []string{provider.StoreFunctions, provider.StoreAssets, provider.StoreCache}

type artifacts struct {
	p *Provider
}

func (a artifacts) bucket(ctx context.Context, class edge.Class) (*storage.BucketHandle, error) {
	if class == "" {
		return nil, ports.Classless("an artifact")
	}
	clients, err := a.p.openClients(ctx)
	if err != nil {
		return nil, err
	}
	client, err := clients.Storage()
	if err != nil {
		return nil, err
	}
	return client.Bucket(clients.Bucket(class)), nil
}

func objectName(ref provider.ArtifactRef) (string, error) {
	for _, store := range artifactStores {
		if ref.Bucket == store {
			return store + "/" + ref.Key, nil
		}
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"this provider keeps no %q store; it keeps %q, %q and %q",
		ref.Bucket, provider.StoreFunctions, provider.StoreAssets, provider.StoreCache)
}

func (a artifacts) object(ctx context.Context, ref provider.ArtifactRef) (*storage.ObjectHandle, error) {
	name, err := objectName(ref)
	if err != nil {
		return nil, err
	}
	bucket, err := a.bucket(ctx, ref.Class)
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
		return a.storeless(ctx, ref.Class, fmt.Errorf("upload %s: %w", object.ObjectName(), err))
	}
	if err := writer.Close(); err != nil {
		return a.storeless(ctx, ref.Class, fmt.Errorf("upload %s: %w", object.ObjectName(), err))
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
		return false, a.storeless(ctx, ref.Class, fmt.Errorf("look for %s: %w", object.ObjectName(), err))
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
		return nil, a.storeless(ctx, ref.Class, fmt.Errorf("read %s: %w", object.ObjectName(), err))
	}
	return reader, nil
}

func (a artifacts) RemovePrefix(ctx context.Context, class edge.Class, prefix string, progress edge.Progress) error {
	if prefix == "" {
		return refusal.Refuse(refusal.CodeInvalid,
			"an empty prefix names every artifact this project keeps")
	}
	bucket, err := a.bucket(ctx, class)
	if err != nil {
		return err
	}
	var errs []error
	for _, store := range artifactStores {
		if err := sweep(ctx, bucket, store+"/"+prefix); err != nil {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if progress != nil {
		progress.Detail("removed " + prefix)
	}
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
			if errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
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

func absent(err error) bool {
	var answered *googleapi.Error
	return errors.As(err, &answered) && answered.Code == http.StatusNotFound
}

func (a artifacts) storeless(ctx context.Context, class edge.Class, err error) error {
	names, named := a.p.Names(ctx)
	if named == nil && (errors.Is(err, storage.ErrBucketNotExist) || absent(err)) {
		return refusal.Refuse(refusal.CodeNotReady,
			"this project has no %s bucket, so it has no %s artifact store yet.\nRun `%s` to create it, then try again",
			names.Bucket(class), class, provider.BootstrapCommand(class))
	}
	return err
}
