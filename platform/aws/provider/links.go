package provider

import (
	"context"
	"fmt"
	"slices"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type publishedLinks struct {
	store values.Store
	class edge.Class
}

func (p publishedLinks) scope(slug string) values.Scope {
	return values.Scope{Project: slug, Class: p.class}
}

func (p publishedLinks) PublishRecords(ctx context.Context, slug, environment, owner string, records []*linksv1.Link) error {
	scope := p.scope(slug)
	publishing := make([]values.Publishing, 0, len(records))
	published := make([]string, 0, len(records))
	for _, record := range records {
		if err := providerkit.VerifyLink(record); err != nil {
			return err
		}
		pair, err := providerkit.LinkPair(owner, record)
		if err != nil {
			return err
		}
		publishing = append(publishing, values.Publishing{Name: record.GetName(), Pair: pair})
		published = append(published, record.GetName())
	}
	if _, err := p.store.SetLinks(ctx, scope, environment, owner, publishing); err != nil {
		return err
	}

	held, err := p.store.ListLinks(ctx, scope, environment)
	if err != nil {
		return err
	}
	stale := make([]string, 0, len(held))
	for _, link := range held {
		if link.Owner != owner || link.Environment != canonical(environment) || slices.Contains(published, link.Name) {
			continue
		}
		stale = append(stale, link.Name)
	}
	if _, err := p.store.RemoveLinks(ctx, scope, environment, stale); err != nil {
		return err
	}
	return nil
}

func (p publishedLinks) ResolveRecords(ctx context.Context, slug, environment string, names []string) ([]deploy.PublishedRecord, error) {
	resolved, err := p.store.ResolveLinks(ctx, p.scope(slug), environment, names)
	if err != nil {
		return nil, err
	}
	out := make([]deploy.PublishedRecord, 0, len(resolved))
	for i, published := range resolved {
		link, err := providerkit.DecodeLink(published.Value)
		if err != nil {
			return nil, fmt.Errorf("read link %s: %v: %w", names[i], err, providerkit.ErrUnreadableRecord)
		}
		out = append(out, deploy.PublishedRecord{Link: link, Version: published.Version})
	}
	return out, nil
}

func (p publishedLinks) PublishedNames(ctx context.Context, slug, class, environment string) ([]string, error) {
	return p.store.PublishedNames(ctx, values.Scope{Project: slug, Class: edge.Class(class)}, environment)
}

func canonical(environment string) string {
	if environment == "" {
		return values.ClassWideEnvironment
	}
	return environment
}

var _ deploy.LinkStore = publishedLinks{}
