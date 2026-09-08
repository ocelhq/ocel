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

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const bucketStem = "ocel-"

func BucketName(project string, class providerkit.Class) string {
	return bucketStem + project + "-" + string(class)
}

var artifactStores = []string{providerkit.StoreFunctions, providerkit.StoreAssets, providerkit.StoreCache}

type artifacts struct {
	clients *clients
}

func (a artifacts) bucket(class providerkit.Class) (*storage.BucketHandle, error) {
	if class == "" {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"an artifact names no class, and this project keeps each class's artifacts in a bucket of its own")
	}
	client, err := a.clients.Storage()
	if err != nil {
		return nil, err
	}
	return client.Bucket(BucketName(a.clients.project, class)), nil
}

func objectName(ref providerkit.ArtifactRef) (string, error) {
	for _, store := range artifactStores {
		if ref.Bucket == store {
			return store + "/" + ref.Key, nil
		}
	}
	return "", providerkit.Refuse(providerkit.CodeInvalid,
		"this provider keeps no %q store; it keeps %q, %q and %q",
		ref.Bucket, providerkit.StoreFunctions, providerkit.StoreAssets, providerkit.StoreCache)
}

func (a artifacts) object(ref providerkit.ArtifactRef) (*storage.ObjectHandle, error) {
	name, err := objectName(ref)
	if err != nil {
		return nil, err
	}
	bucket, err := a.bucket(ref.Class)
	if err != nil {
		return nil, err
	}
	return bucket.Object(name), nil
}

func (a artifacts) Put(ctx context.Context, ref providerkit.ArtifactRef, body io.Reader) error {
	object, err := a.object(ref)
	if err != nil {
		return err
	}
	writer := object.NewWriter(ctx)
	if _, err := io.Copy(writer, body); err != nil {
		writer.Close()
		return storeless(ref.Class, fmt.Errorf("upload %s: %w", object.ObjectName(), err))
	}
	if err := writer.Close(); err != nil {
		return storeless(ref.Class, fmt.Errorf("upload %s: %w", object.ObjectName(), err))
	}
	return nil
}

func (a artifacts) Has(ctx context.Context, ref providerkit.ArtifactRef) (bool, error) {
	object, err := a.object(ref)
	if err != nil {
		return false, err
	}
	if _, err := object.Attrs(ctx); err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return false, nil
		}
		return false, storeless(ref.Class, fmt.Errorf("look for %s: %w", object.ObjectName(), err))
	}
	return true, nil
}

func (a artifacts) Open(ctx context.Context, ref providerkit.ArtifactRef) (io.ReadCloser, error) {
	object, err := a.object(ref)
	if err != nil {
		return nil, err
	}
	reader, err := object.NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, providerkit.Refuse(providerkit.CodeInvalid, "no artifact at %s", object.ObjectName())
		}
		return nil, storeless(ref.Class, fmt.Errorf("read %s: %w", object.ObjectName(), err))
	}
	return reader, nil
}

func (a artifacts) RemovePrefix(ctx context.Context, class providerkit.Class, prefix string, report providerkit.Reporter) error {
	if prefix == "" {
		return providerkit.Refuse(providerkit.CodeInvalid,
			"an empty prefix names every artifact this project keeps")
	}
	bucket, err := a.bucket(class)
	if err != nil {
		return err
	}
	var errs []error
	for _, store := range artifactStores {
		if err := sweep(ctx, bucket, store+"/"+prefix); err != nil {
			errs = append(errs, err)
		}
	}
	if report != nil {
		report.Detail("removed " + prefix)
	}
	return errors.Join(errs...)
}

func sweep(ctx context.Context, bucket *storage.BucketHandle, prefix string) error {
	held := bucket.Objects(ctx, &storage.Query{Prefix: prefix})
	for {
		attrs, err := held.Next()
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

func storeless(class providerkit.Class, err error) error {
	if errors.Is(err, storage.ErrBucketNotExist) || absent(err) {
		return providerkit.Refuse(providerkit.CodeNotReady,
			"this project has no %s artifact store yet.\nRun `%s` to create it, then try again",
			class, providerkit.BootstrapCommand(class))
	}
	return err
}
