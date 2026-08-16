package server

import (
	"context"
	"errors"
	"fmt"

	connect "connectrpc.com/connect"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
)

func (s *Server) SetLink(ctx context.Context, req *deploymentsv1.SetLinkRequest) (*deploymentsv1.SetLinkResponse, error) {
	if err := linkTarget(req.GetClass(), req.GetSlug(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	if err := invalidArgument(vars.ValidateLinkName(req.GetSlug(), req.GetEnvironment(), req.GetLink().GetName())); err != nil {
		return nil, err
	}
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
	if err != nil {
		return nil, err
	}

	version, err := store.SetLink(ctx, req.GetSlug(), req.GetOwner(), req.GetEnvironment(), req.GetLink())
	if err != nil {
		return nil, linksError(err)
	}
	return &deploymentsv1.SetLinkResponse{Version: uint64(version)}, nil
}

func (s *Server) RemoveLink(ctx context.Context, req *deploymentsv1.RemoveLinkRequest) (*deploymentsv1.RemoveLinkResponse, error) {
	if err := linkTarget(req.GetClass(), req.GetSlug(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	if err := invalidArgument(vars.ValidateLinkName(req.GetSlug(), req.GetEnvironment(), req.GetName())); err != nil {
		return nil, err
	}
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
	if err != nil {
		return nil, err
	}

	removed, err := store.RemoveLink(ctx, req.GetSlug(), req.GetEnvironment(), req.GetName())
	if err != nil {
		return nil, linksError(err)
	}
	return &deploymentsv1.RemoveLinkResponse{Removed: removed}, nil
}

func (s *Server) ListLinks(ctx context.Context, req *deploymentsv1.ListLinksRequest) (*deploymentsv1.ListLinksResponse, error) {
	if err := linkTarget(req.GetClass(), req.GetSlug(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	store, err := s.store(ctx, req.GetOptions(), req.GetClass())
	if err != nil {
		return nil, err
	}

	found, err := store.ListLinks(ctx, req.GetSlug(), req.GetEnvironment())
	if err != nil {
		return nil, linksError(err)
	}
	resp := &deploymentsv1.ListLinksResponse{Links: make([]*deploymentsv1.LinkSummary, 0, len(found))}
	for _, l := range found {
		resp.Links = append(resp.Links, &deploymentsv1.LinkSummary{
			Name:    l.Name,
			Type:    l.Type,
			Source:  l.Source,
			Owner:   l.Owner,
			Version: uint64(l.Version),
		})
	}
	return resp, nil
}

func linkTarget(class deploymentsv1.Environment_Class, slug, environment string) error {
	preview := class == deploymentsv1.Environment_CLASS_PREVIEW
	if !preview && class != deploymentsv1.Environment_CLASS_PRODUCTION {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"class %s is neither %q nor %q; a link is published to an ocel coordinate, never to a stage or a stack name",
			class, bootstrap.ClassProduction, bootstrap.ClassPreview))
	}
	if err := invalidArgument(vars.ValidateLinkTarget(slug, environment)); err != nil {
		return err
	}
	if environment != "" && !preview {
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
	case errors.Is(err, vars.ErrUnsourced), errors.Is(err, vars.ErrUnreadableRecord), errors.Is(err, vars.ErrUnscopedGrant):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, vars.ErrNotPublished):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return varsError(err)
	}
}
