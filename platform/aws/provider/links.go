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
	published := make([]string, 0, len(records))
	for _, record := range records {
		if err := providerkit.VerifyLink(record); err != nil {
			return err
		}
		pair, err := providerkit.LinkPair(owner, record)
		if err != nil {
			return err
		}
		if _, err := p.store.SetLink(ctx, scope, environment, owner, record.GetName(), pair); err != nil {
			return err
		}
		published = append(published, record.GetName())
	}

	held, err := p.store.ListLinks(ctx, scope, environment)
	if err != nil {
		return err
	}
	for _, link := range held {
		if link.Owner != owner || link.Environment != canonical(environment) || slices.Contains(published, link.Name) {
			continue
		}
		if _, err := p.store.RemoveLink(ctx, scope, environment, link.Name); err != nil {
			return err
		}
	}
	return nil
}

func (p publishedLinks) ResolveRecords(ctx context.Context, slug, environment string, names []string) ([]deploy.PublishedRecord, error) {
	scope := p.scope(slug)
	out := make([]deploy.PublishedRecord, 0, len(names))
	for _, name := range names {
		published, err := p.store.ResolveLink(ctx, scope, environment, name)
		if err != nil {
			return nil, err
		}
		link, err := providerkit.DecodeLink(published.Value)
		if err != nil {
			return nil, fmt.Errorf("read link %s: %v: %w", name, err, providerkit.ErrUnreadableRecord)
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
