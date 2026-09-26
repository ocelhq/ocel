package envvarsserver

import (
	"context"
	"errors"

	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
)

func (h *Service) SetValue(ctx context.Context, req *envvarsv1.SetValueRequest) (*envvarsv1.SetValueResponse, error) {
	if err := h.addressable(ctx, req.GetTier(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	metadata, err := store.Set(ctx, scope, coordinateOf(req.GetCoordinate()), req.GetValue(), req.ExpectedVersion)
	if err != nil {
		return nil, valuesError(err)
	}
	return &envvarsv1.SetValueResponse{Metadata: metadataProto(scope, metadata)}, nil
}

func (h *Service) ListValues(ctx context.Context, req *envvarsv1.ListValuesRequest) (*envvarsv1.ListValuesResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	found, err := store.List(ctx, scope)
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &envvarsv1.ListValuesResponse{Values: make([]*envvarsv1.ValueMetadata, 0, len(found))}
	for _, m := range found {
		resp.Values = append(resp.Values, metadataProto(scope, m))
	}
	return resp, nil
}

func (h *Service) GetValue(ctx context.Context, req *envvarsv1.GetValueRequest) (*envvarsv1.GetValueResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	value, err := store.Get(ctx, scope, coordinateOf(req.GetCoordinate()), req.GetReveal())
	if errors.Is(err, envvars.ErrNotFound) {
		return &envvarsv1.GetValueResponse{}, nil
	}
	if err != nil {
		return nil, valuesError(err)
	}
	return &envvarsv1.GetValueResponse{
		Found:    true,
		Metadata: metadataProto(scope, value.Metadata),
		Value:    value.Plaintext,
	}, nil
}

func (h *Service) RevealValues(ctx context.Context, req *envvarsv1.RevealValuesRequest) (*envvarsv1.RevealValuesResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	cells := make([]envvars.Coordinate, 0, len(req.GetCells()))
	for _, c := range req.GetCells() {
		cells = append(cells, coordinateOf(c))
	}
	found, err := store.Reveal(ctx, scope, cells)
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &envvarsv1.RevealValuesResponse{Values: make([]*envvarsv1.RevealedValue, 0, len(found))}
	for _, v := range found {
		resp.Values = append(resp.Values, &envvarsv1.RevealedValue{
			Metadata: metadataProto(scope, v.Metadata),
			Value:    v.Plaintext,
		})
	}
	return resp, nil
}

func (h *Service) DeleteValue(ctx context.Context, req *envvarsv1.DeleteValueRequest) (*envvarsv1.DeleteValueResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	deleted, err := store.Delete(ctx, scope, coordinateOf(req.GetCoordinate()), req.ExpectedVersion)
	if err != nil {
		return nil, valuesError(err)
	}
	return &envvarsv1.DeleteValueResponse{Deleted: deleted}, nil
}

func (h *Service) ListVersions(ctx context.Context, req *envvarsv1.ListVersionsRequest) (*envvarsv1.ListVersionsResponse, error) {
	store, scope, err := h.scoped(req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	history, err := store.Versions(ctx, scope, coordinateOf(req.GetCoordinate()))
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &envvarsv1.ListVersionsResponse{Versions: make([]*envvarsv1.VersionEntry, 0, len(history))}
	for _, v := range history {
		resp.Versions = append(resp.Versions, &envvarsv1.VersionEntry{
			Version:   v.Version,
			CreatedAt: v.CreatedAt,
			Size:      v.Size,
		})
	}
	return resp, nil
}
