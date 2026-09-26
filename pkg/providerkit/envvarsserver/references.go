package envvarsserver

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"

	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
)

func (h *Service) SetReference(ctx context.Context, req *envvarsv1.SetReferenceRequest) (*envvarsv1.SetReferenceResponse, error) {
	if err := h.addressable(ctx, req.GetTier(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	target := req.GetTarget()
	if target.GetEnvironment() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"a reference resolves against the value %s sets for all environments; %q is an environment of the project that owns the reference, and names nothing in the target's",
			target.GetKey(), target.GetEnvironment()))
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	metadata, err := store.SetReference(ctx, scope, coordinateOf(req.GetCoordinate()), envvars.Target{
		Project: target.GetSlug(),
		Cell:    envvars.Cell{Folder: target.GetFolder(), Key: target.GetKey()},
	})
	if err != nil {
		return nil, valuesError(err)
	}
	return &envvarsv1.SetReferenceResponse{Metadata: metadataProto(scope, metadata)}, nil
}

func (h *Service) ListReferences(ctx context.Context, req *envvarsv1.ListReferencesRequest) (*envvarsv1.ListReferencesResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	found, err := store.References(ctx, scope, coordinateOf(req.GetCoordinate()))
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &envvarsv1.ListReferencesResponse{References: make([]*envvarsv1.Coordinate, 0, len(found))}
	for _, r := range found {
		resp.References = append(resp.References, coordinateProto(r.Project, r.Coordinate))
	}
	return resp, nil
}
