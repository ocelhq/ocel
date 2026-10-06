package gcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
)

const invalidateTagsPerRequest = 10

func (p *Provider) InvalidateTags(ctx context.Context, urlMap string, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	engine, err := clients.Compute()
	if err != nil {
		return err
	}
	for chunk := range slices.Chunk(tags, invalidateTagsPerRequest) {
		_, err := attempted(ctx, func(call ...googleapi.CallOption) (*compute.Operation, error) {
			return engine.UrlMaps.InvalidateCache(clients.project, urlMap, &compute.CacheInvalidationRule{CacheTags: chunk}).Context(ctx).Do(call...)
		})
		if err != nil {
			return fmt.Errorf("invalidate cache tags %s on url map %s: %w", strings.Join(chunk, ","), urlMap, err)
		}
	}
	return nil
}
