package server

import (
	"context"
	"errors"
	"fmt"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
)

func (s *VarsServer) SetLink(ctx context.Context, req *envvarsv1.SetLinkRequest) (*envvarsv1.SetLinkResponse, error) {
	if err := linkTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	if err := invalidArgument(vars.ValidateLinkName(req.GetSlug(), req.GetEnvironment(), req.GetLink().GetName())); err != nil {
		return nil, err
	}
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}

	version, err := store.SetLink(ctx, req.GetSlug(), req.GetOwner(), req.GetEnvironment(), req.GetLink())
	if err != nil {
		return nil, linksError(err)
	}
	return &envvarsv1.SetLinkResponse{Version: uint64(version)}, nil
}

func (s *VarsServer) RemoveLink(ctx context.Context, req *envvarsv1.RemoveLinkRequest) (*envvarsv1.RemoveLinkResponse, error) {
	if err := linkTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	if err := invalidArgument(vars.ValidateLinkName(req.GetSlug(), req.GetEnvironment(), req.GetName())); err != nil {
		return nil, err
	}
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}

	removed, err := store.RemoveLink(ctx, req.GetSlug(), req.GetEnvironment(), req.GetName())
	if err != nil {
		return nil, linksError(err)
	}
	return &envvarsv1.RemoveLinkResponse{Removed: removed}, nil
}

func (s *VarsServer) ListLinks(ctx context.Context, req *envvarsv1.ListLinksRequest) (*envvarsv1.ListLinksResponse, error) {
	if err := linkTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	store, err := s.store(ctx, s.config.get().Region, req.GetTier())
	if err != nil {
		return nil, err
	}

	found, err := store.ListLinks(ctx, req.GetSlug(), req.GetEnvironment())
	if err != nil {
		return nil, linksError(err)
	}
	resp := &envvarsv1.ListLinksResponse{Links: make([]*envvarsv1.LinkSummary, 0, len(found))}
	for _, l := range found {
		resp.Links = append(resp.Links, &envvarsv1.LinkSummary{
			Name:       l.Name,
			Type:       l.Type,
			Source:     l.Source,
			Owner:      l.Owner,
			Version:    uint64(l.Version),
			Properties: naming.PropertyShapeMessages(l.Properties),
		})
	}
	return resp, nil
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
	case errors.Is(err, vars.ErrClaimed):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, vars.ErrUnsourced), errors.Is(err, vars.ErrUnreadableRecord),
		errors.Is(err, vars.ErrUnscopedGrant), errors.Is(err, vars.ErrUnattachedGrant):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, vars.ErrNotPublished):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return varsError(err)
	}
}
