package gcp

import (
	"context"
	"errors"
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

func (p *Provider) InvalidateHostnames(ctx context.Context, urlMap string, hostnames []string) error {
	if len(hostnames) == 0 {
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
	var failed []error
	for _, hostname := range hostnames {
		_, err := attempted(ctx, func(call ...googleapi.CallOption) (*compute.Operation, error) {
			return engine.UrlMaps.InvalidateCache(clients.project, urlMap, &compute.CacheInvalidationRule{Host: hostname, Path: "/*"}).Context(ctx).Do(call...)
		})
		if err != nil {
			failed = append(failed, fmt.Errorf("invalidate every path of %s on url map %s: %w", hostname, urlMap, err))
		}
	}
	return errors.Join(failed...)
}
