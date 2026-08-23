package server

import (
	"context"
	"errors"
	"fmt"
	"slices"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *VarsServer) SetLink(ctx context.Context, req *envvarsv1.SetLinkRequest) (*envvarsv1.SetLinkResponse, error) {
	if err := linkTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	link := req.GetLink()
	if err := invalidArgument(values.ValidateLinkName(req.GetEnvironment(), link.GetName())); err != nil {
		return nil, err
	}
	if err := invalidArgument(providerkit.ValidatePublisher(req.GetOwner())); err != nil {
		return nil, err
	}
	if err := providerkit.RefuseUnsourced(req.GetOwner(), link); err != nil {
		return nil, linksError(err)
	}
	if err := providerkit.VerifyLink(link); err != nil {
		return nil, linksError(err)
	}
	pair, err := providerkit.LinkPair(req.GetOwner(), link)
	if err != nil {
		return nil, linksError(err)
	}

	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	version, err := store.SetLink(ctx, scope, req.GetEnvironment(), req.GetOwner(), link.GetName(), pair)
	if err != nil {
		return nil, linksError(err)
	}
	return &envvarsv1.SetLinkResponse{Version: uint64(version)}, nil
}

func (s *VarsServer) RemoveLink(ctx context.Context, req *envvarsv1.RemoveLinkRequest) (*envvarsv1.RemoveLinkResponse, error) {
	if err := linkTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	if err := invalidArgument(values.ValidateLinkName(req.GetEnvironment(), req.GetName())); err != nil {
		return nil, err
	}
	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}

	removed, err := store.RemoveLink(ctx, scope, req.GetEnvironment(), req.GetName())
	if err != nil {
		return nil, linksError(err)
	}
	return &envvarsv1.RemoveLinkResponse{Removed: removed}, nil
}

func (s *VarsServer) ListLinks(ctx context.Context, req *envvarsv1.ListLinksRequest) (*envvarsv1.ListLinksResponse, error) {
	if err := linkTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	store, scope, err := s.scoped(ctx, s.config.get().Region, req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}

	found, err := store.ListLinks(ctx, scope, req.GetEnvironment())
	if err != nil {
		return nil, linksError(err)
	}
	resp := &envvarsv1.ListLinksResponse{Links: make([]*envvarsv1.LinkSummary, 0, len(found))}
	for _, published := range found {
		link, err := providerkit.DecodeLink(published.Record)
		if err != nil {
			return nil, linksError(fmt.Errorf("read link %s's record: %v: %w", published.Name, err, providerkit.ErrUnreadableRecord))
		}
		shapes, err := providerkit.DecodeShapes(published.Shapes)
		if err != nil {
			return nil, linksError(fmt.Errorf("read link %s's shape: %v: %w", published.Name, err, providerkit.ErrUnreadableRecord))
		}
		resp.Links = append(resp.Links, &envvarsv1.LinkSummary{
			Name:       published.Name,
			Type:       naming.LinkTypeOf(link),
			Source:     link.GetSource(),
			Owner:      published.Owner,
			Version:    uint64(published.Version),
			Properties: naming.PropertyShapeMessages(shapes),
		})
	}
	return resp, nil
}

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

func linkTarget(tier environmentv1.Tier, environment string) error {
	if environment != "" && tier != environmentv1.Tier_TIER_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"environment %q is named alongside class %q: an ocel coordinate is a class and, in %s, one preview environment; leave the environment off",
			environment, bootstrap.ClassProduction, bootstrap.ClassPreview))
	}
	return nil
}

func invalidArgument(err error) error {
	if err == nil {
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument, err)
}

func linksError(err error) error {
	switch {
	case errors.Is(err, values.ErrClaimed):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, values.ErrTornPair):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, providerkit.ErrUnsourced), errors.Is(err, providerkit.ErrUnreadableRecord),
		errors.Is(err, providerkit.ErrUnscopedGrant), errors.Is(err, providerkit.ErrUnattachedGrant):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, values.ErrNotPublished):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return varsError(err)
	}
}
