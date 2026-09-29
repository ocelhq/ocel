package variablestoreserver

import (
	"context"
	"errors"
	"slices"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func (h *Service) SetValue(ctx context.Context, req *variablestorev1.SetValueRequest) (*variablestorev1.SetValueResponse, error) {
	if err := h.addressable(ctx, req.GetTier(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	if err := refuseEnvSourceOwned(ctx, store, scope, coordinateOf(req.GetCoordinate()), false); err != nil {
		return nil, err
	}
	metadata, err := store.Set(ctx, scope, coordinateOf(req.GetCoordinate()), req.GetValue(), req.ExpectedVersion)
	if err != nil {
		return nil, valuesError(err)
	}
	return &variablestorev1.SetValueResponse{Metadata: metadataProto(scope, metadata)}, nil
}

func (h *Service) ListValues(ctx context.Context, req *variablestorev1.ListValuesRequest) (*variablestorev1.ListValuesResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	found, err := store.List(ctx, scope)
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &variablestorev1.ListValuesResponse{Values: make([]*variablestorev1.ValueMetadata, 0, len(found))}
	for _, m := range found {
		resp.Values = append(resp.Values, metadataProto(scope, m))
	}
	return resp, nil
}

func (h *Service) GetValue(ctx context.Context, req *variablestorev1.GetValueRequest) (*variablestorev1.GetValueResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	value, err := store.Get(ctx, scope, coordinateOf(req.GetCoordinate()), req.GetReveal())
	if errors.Is(err, variablestore.ErrNotFound) {
		return &variablestorev1.GetValueResponse{}, nil
	}
	if err != nil {
		return nil, valuesError(err)
	}
	return &variablestorev1.GetValueResponse{
		Found:    true,
		Metadata: metadataProto(scope, value.Metadata),
		Value:    value.Plaintext,
	}, nil
}

func (h *Service) RevealValues(ctx context.Context, req *variablestorev1.RevealValuesRequest) (*variablestorev1.RevealValuesResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	cells := make([]variablestore.Coordinate, 0, len(req.GetCells()))
	for _, c := range req.GetCells() {
		cells = append(cells, coordinateOf(c))
	}
	found, err := store.Reveal(ctx, scope, cells)
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &variablestorev1.RevealValuesResponse{Values: make([]*variablestorev1.RevealedValue, 0, len(found))}
	for _, v := range found {
		resp.Values = append(resp.Values, &variablestorev1.RevealedValue{
			Metadata: metadataProto(scope, v.Metadata),
			Value:    v.Plaintext,
		})
	}
	return resp, nil
}

func (h *Service) DeleteValue(ctx context.Context, req *variablestorev1.DeleteValueRequest) (*variablestorev1.DeleteValueResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	if err := refuseEnvSourceOwned(ctx, store, scope, coordinateOf(req.GetCoordinate()), true); err != nil {
		return nil, err
	}
	deleted, err := store.Delete(ctx, scope, coordinateOf(req.GetCoordinate()), req.ExpectedVersion)
	if err != nil {
		return nil, valuesError(err)
	}
	return &variablestorev1.DeleteValueResponse{Deleted: deleted}, nil
}

func (h *Service) ListVersions(ctx context.Context, req *variablestorev1.ListVersionsRequest) (*variablestorev1.ListVersionsResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	history, err := store.Versions(ctx, scope, coordinateOf(req.GetCoordinate()))
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &variablestorev1.ListVersionsResponse{Versions: make([]*variablestorev1.VersionEntry, 0, len(history))}
	for _, v := range slices.Backward(history) {
		resp.Versions = append(resp.Versions, &variablestorev1.VersionEntry{
			Version:   v.Version,
			CreatedAt: v.CreatedAt,
			Size:      v.Size,
		})
	}
	return resp, nil
}
