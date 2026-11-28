package variablestoreserver

import (
	"context"
	"errors"
	"slices"
	"strings"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func (h *Service) SetValues(ctx context.Context, req *variablestorev1.SetValuesRequest) (*variablestorev1.SetValuesResponse, error) {
	writes := make([]variablestore.ValueWrite, 0, len(req.GetValues()))
	environments := make([]string, 0, len(req.GetValues()))
	for _, value := range req.GetValues() {
		at := coordinateOf(value.GetCoordinate())
		writes = append(writes, variablestore.ValueWrite{At: at, Plaintext: value.GetValue(), Expected: value.ExpectedVersion, Class: classOf(value.GetClass())})
		environments = append(environments, at.Environment)
	}
	if err := h.addressable(ctx, req.GetTier(), req.GetSlug(), environments...); err != nil {
		return nil, err
	}
	store, scope, err := h.scoped(ctx, req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	coordinates := make([]variablestore.Coordinate, 0, len(writes))
	for _, write := range writes {
		coordinates = append(coordinates, write.At)
	}
	if err := refuseEnvSourceOwned(ctx, store, scope, false, coordinates...); err != nil {
		return nil, err
	}
	written, err := store.Set(ctx, scope, writes)
	if err != nil {
		return nil, valuesError(err)
	}
	resp := &variablestorev1.SetValuesResponse{Written: make([]*variablestorev1.ValueMetadata, 0, len(written))}
	for _, metadata := range written {
		resp.Written = append(resp.Written, metadataProto(scope, metadata))
	}
	return resp, nil
}

func (h *Service) ListValues(ctx context.Context, req *variablestorev1.ListValuesRequest) (*variablestorev1.ListValuesResponse, error) {
	store, scope, err := h.scoped(ctx, req.GetTier(), req.GetSlug())
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
	store, scope, err := h.scoped(ctx, req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	value, err := store.Get(ctx, scope, coordinateOf(req.GetCoordinate()), false)
	if errors.Is(err, variablestore.ErrNotFound) {
		return &variablestorev1.GetValueResponse{}, nil
	}
	if err != nil {
		return nil, valuesError(err)
	}
	return &variablestorev1.GetValueResponse{
		Found:    true,
		Metadata: metadataProto(scope, value.Metadata),
	}, nil
}

func (h *Service) RevealValues(ctx context.Context, req *variablestorev1.RevealValuesRequest) (*variablestorev1.RevealValuesResponse, error) {
	found, scope, err := h.open(ctx, req.GetTier(), req.GetSlug(), req.GetCells())
	if err != nil {
		return nil, err
	}
	if err := refuseSecrets(found); err != nil {
		return nil, err
	}
	return &variablestorev1.RevealValuesResponse{Values: revealedProto(scope, found)}, nil
}

func (h *Service) DeliverValues(ctx context.Context, req *variablestorev1.DeliverValuesRequest) (*variablestorev1.DeliverValuesResponse, error) {
	found, scope, err := h.open(ctx, req.GetTier(), req.GetSlug(), req.GetCells())
	if err != nil {
		return nil, err
	}
	return &variablestorev1.DeliverValuesResponse{Values: revealedProto(scope, found)}, nil
}

func (h *Service) open(ctx context.Context, tier environmentv1.Tier, slug string, named []*variablestorev1.Coordinate) ([]variablestore.Value, variablestore.Scope, error) {
	store, scope, err := h.scoped(ctx, tier, slug)
	if err != nil {
		return nil, variablestore.Scope{}, err
	}
	cells := make([]variablestore.Coordinate, 0, len(named))
	for _, c := range named {
		cells = append(cells, coordinateOf(c))
	}
	found, err := store.Reveal(ctx, scope, cells)
	if err != nil {
		return nil, variablestore.Scope{}, valuesError(err)
	}
	return found, scope, nil
}

func refuseSecrets(found []variablestore.Value) error {
	var secrets []string
	for _, v := range found {
		if v.Class == variablestore.ClassSecret {
			secrets = append(secrets, v.Coordinate.String())
		}
	}
	if len(secrets) == 0 {
		return nil
	}
	named := secrets[0] + " is a secret"
	if len(secrets) > 1 {
		named = strings.Join(secrets, ", ") + " are secrets"
	}
	return provider.RefusalError(refusal.Refuse(refusal.CodeDenied,
		"%s, and a secret is never revealed once set: only a deploy reads it. Set it again to change it", named))
}

func revealedProto(scope variablestore.Scope, found []variablestore.Value) []*variablestorev1.RevealedValue {
	out := make([]*variablestorev1.RevealedValue, 0, len(found))
	for _, v := range found {
		out = append(out, &variablestorev1.RevealedValue{
			Metadata: metadataProto(scope, v.Metadata),
			Value:    v.Plaintext,
		})
	}
	return out
}

func (h *Service) DeleteValue(ctx context.Context, req *variablestorev1.DeleteValueRequest) (*variablestorev1.DeleteValueResponse, error) {
	store, scope, err := h.scoped(ctx, req.GetTier(), req.GetCoordinate().GetSlug())
	if err != nil {
		return nil, err
	}
	if err := refuseEnvSourceOwned(ctx, store, scope, true, coordinateOf(req.GetCoordinate())); err != nil {
		return nil, err
	}
	deleted, err := store.Delete(ctx, scope, coordinateOf(req.GetCoordinate()), req.ExpectedVersion)
	if err != nil {
		return nil, valuesError(err)
	}
	return &variablestorev1.DeleteValueResponse{Deleted: deleted}, nil
}

func (h *Service) ListVersions(ctx context.Context, req *variablestorev1.ListVersionsRequest) (*variablestorev1.ListVersionsResponse, error) {
	store, scope, err := h.scoped(ctx, req.GetTier(), req.GetCoordinate().GetSlug())
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
