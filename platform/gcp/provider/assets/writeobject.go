package assets

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

func (p *Store) CreateObject(ctx context.Context, tier environment.Tier, key string, attrs storage.ObjectAttrs, body []byte) error {
	return p.writeObject(ctx, tier, key, attrs, body, &storage.Conditions{DoesNotExist: true})
}

func (p *Store) replaceObject(ctx context.Context, tier environment.Tier, key string, attrs storage.ObjectAttrs, body []byte) error {
	return p.writeObject(ctx, tier, key, attrs, body, nil)
}

func (p *Store) writeObject(ctx context.Context, tier environment.Tier, key string, attrs storage.ObjectAttrs, body []byte, conditions *storage.Conditions) error {
	objects := p
	object, err := objects.object(ctx, provider.ArtifactRef{Tier: tier, Key: key})
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
	return fmt.Errorf("upload %s: %w", object.ObjectName(), err)
}
