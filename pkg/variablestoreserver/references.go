package variablestoreserver

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func (h *Service) SetReference(ctx context.Context, req *variablestorev1.SetReferenceRequest) (*variablestorev1.SetReferenceResponse, error) {
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
	if err := refuseEnvSourceOwned(ctx, store, scope, coordinateOf(req.GetCoordinate()), false); err != nil {
		return nil, err
	}
	metadata, err := store.SetReference(ctx, scope, coordinateOf(req.GetCoordinate()), variablestore.Target{
		Project: target.GetSlug(),
		Cell:    variablestore.Cell{Folder: target.GetFolder(), Key: target.GetKey()},
	})
	if err != nil {
		return nil, valuesError(err)
	}
	return &variablestorev1.SetReferenceResponse{Metadata: metadataProto(scope, metadata)}, nil
}

func (h *Service) ListReferences(ctx context.Context, req *variablestorev1.ListReferencesRequest) (*variablestorev1.ListReferencesResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	found, err := store.References(ctx, scope, coordinateOf(req.GetCoordinate()))
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &variablestorev1.ListReferencesResponse{References: make([]*variablestorev1.Coordinate, 0, len(found))}
	for _, r := range found {
		resp.References = append(resp.References, coordinateProto(r.Project, r.Coordinate))
	}
	return resp, nil
}
