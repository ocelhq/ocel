package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const singleRequestUploadLimit = 32 << 20

func (p *Provider) createObject(ctx context.Context, tier environment.Tier, store, key string, attrs storage.ObjectAttrs, body []byte) error {
	return p.writeObject(ctx, tier, store, key, attrs, body, &storage.Conditions{DoesNotExist: true})
}

func (p *Provider) replaceObject(ctx context.Context, tier environment.Tier, store, key string, attrs storage.ObjectAttrs, body []byte) error {
	return p.writeObject(ctx, tier, store, key, attrs, body, nil)
}

func (p *Provider) writeObject(ctx context.Context, tier environment.Tier, store, key string, attrs storage.ObjectAttrs, body []byte, conditions *storage.Conditions) error {
	objects := artifacts{p}
	object, err := objects.object(ctx, provider.ArtifactRef{Tier: tier, Bucket: store, Key: key})
	if err != nil {
		return err
	}
	if conditions != nil {
		object = object.If(*conditions)
	}
	writer := object.NewWriter(ctx)
	writer.ContentType = attrs.ContentType
	writer.CacheControl = attrs.CacheControl
	if len(body) < singleRequestUploadLimit {
		writer.ChunkSize = len(body) + 1
	}
	_, err = writer.Write(body)
	if closed := writer.Close(); err == nil {
		err = closed
	}
	var failure *googleapi.Error
	switch {
	case err == nil, errors.As(err, &failure) && failure.Code == http.StatusPreconditionFailed:
		return nil
	}
	return objects.storeless(ctx, tier, fmt.Errorf("upload %s: %w", object.ObjectName(), err))
}
