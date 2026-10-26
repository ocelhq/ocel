package variablestoreserver

import (
	"context"
	"errors"
	"slices"

	"github.com/ocelhq/ocel/pkg/envsource"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func (h *Service) PublishValues(ctx context.Context, req *variablestorev1.PublishValuesRequest) (*variablestorev1.PublishValuesResponse, error) {
	if err := bindingTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	listed, err := store.List(ctx, scope)
	if err != nil {
		return nil, valuesError(err)
	}
	current := map[variablestore.Coordinate]variablestore.Metadata{}
	for _, metadata := range listed {
		if metadata.Coordinate.Environment == req.GetEnvironment() {
			current[metadata.Coordinate] = metadata
		}
	}

	var registration envsource.Registration
	registered := false
	if req.GetEnvironment() == "" {
		if registration, registered, err = envsource.Registered(ctx, store.KeyValues, scope.Tier, scope.Project); err != nil {
			return nil, registrationError(err)
		}
	}
	publisher := req.GetPublisher()
	values := req.GetPublished().GetValues()
	coordinates := make([]variablestore.Coordinate, len(values))
	for i, value := range values {
		at := variablestore.Coordinate{Cell: variablestore.Cell{Key: value.GetKey()}, Environment: req.GetEnvironment()}
		coordinates[i] = at
		if err := variablestore.RefuseWrite(at, current[at], value.GetValue(), variablestore.Provenance{Publisher: publisher}); err != nil {
			return nil, valuesError(err)
		}
		if registered {
			if err := refuseRegisteredEnvSource(ctx, store, scope, registration, at, false); err != nil {
				return nil, err
			}
		}
	}

	resp := &variablestorev1.PublishValuesResponse{}
	for i, value := range values {
		from := variablestore.Provenance{Publisher: publisher, PublishedSecret: value.GetSecret()}
		written, err := store.SetPublished(ctx, scope, coordinates[i], value.GetValue(), from, current[coordinates[i]].Version)
		if err != nil {
			return nil, valuesError(err)
		}
		resp.Written = append(resp.Written, metadataProto(scope, written))
	}
	for _, stored := range listed {
		at := stored.Coordinate
		if at.Environment != req.GetEnvironment() || stored.Provenance.Publisher != publisher || slices.Contains(coordinates, at) {
			continue
		}
		removed, err := store.DeletePublished(ctx, scope, at, variablestore.Provenance{Publisher: publisher}, stored.Version)
		if err != nil {
			return nil, valuesError(err)
		}
		if removed {
			resp.Removed = append(resp.Removed, coordinateProto(scope.Project, at))
		}
	}
	return resp, nil
}

func (h *Service) UnpublishValues(ctx context.Context, req *variablestorev1.UnpublishValuesRequest) (*variablestorev1.UnpublishValuesResponse, error) {
	if err := bindingTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	from := variablestore.Provenance{Publisher: req.GetPublisher()}
	resp := &variablestorev1.UnpublishValuesResponse{}
	for _, written := range req.GetPublished().GetValues() {
		at := variablestore.Coordinate{Cell: variablestore.Cell{Key: written.GetKey()}, Environment: req.GetEnvironment()}
		removed, err := store.DeletePublished(ctx, scope, at, from, written.GetVersion())
		if errors.Is(err, variablestore.ErrStaleVersion) {
			continue
		}
		if err != nil {
			return nil, valuesError(err)
		}
		if removed {
			resp.Removed = append(resp.Removed, coordinateProto(scope.Project, at))
		}
	}
	return resp, nil
}
