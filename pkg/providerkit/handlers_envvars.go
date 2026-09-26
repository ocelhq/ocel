package providerkit

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (h *VarsService) values(tier environmentv1.Tier) (envvars.Store, edge.Class, error) {
	vars, err := h.Source.Read()
	if err != nil {
		return envvars.Store{}, "", err
	}
	class := edge.ClassProduction
	if tier == environmentv1.Tier_TIER_PREVIEW {
		class = edge.ClassPreview
	}
	return envvars.Store{Records: vars.Records, Cipher: vars.Cipher}, class, nil
}

func (h *VarsService) verifyGrants(ctx context.Context, binding *bindingsv1.Binding) error {
	if len(binding.GetGrants()) == 0 {
		return nil
	}
	vars, err := h.Source.Read()
	if err != nil {
		return err
	}
	if vars.VerifyGrants == nil {
		return nil
	}
	return vars.VerifyGrants(ctx, bindingOf(binding))
}

func (h *VarsService) scoped(tier environmentv1.Tier, slug string) (envvars.Store, envvars.Scope, error) {
	store, class, err := h.values(tier)
	if err != nil {
		return envvars.Store{}, envvars.Scope{}, err
	}
	if err := envvars.ValidateProject(slug); err != nil {
		return envvars.Store{}, envvars.Scope{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return store, envvars.Scope{Project: slug, Class: class}, nil
}

func (h *VarsService) addressable(ctx context.Context, tier environmentv1.Tier, at *envvarsv1.Coordinate) error {
	environment := at.GetEnvironment()
	if environment == "" {
		return nil
	}
	if tier != environmentv1.Tier_TIER_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"production has a single environment, so %q addresses no value a production function could read", environment))
	}
	named, err := h.namedEnvironments(ctx, at.GetSlug())
	if err != nil {
		return err
	}
	if slices.Contains(named, environment) {
		return nil
	}
	if len(named) == 0 {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"no preview environment named %q exists, and this project has none at all; deploy one with `ocel preview` before setting a value only it would read", environment))
	}
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"no preview environment named %q exists, so nothing would ever read that value. This project's environments are: %s",
		environment, strings.Join(named, ", ")))
}

func (h *VarsService) namedEnvironments(ctx context.Context, slug string) ([]string, error) {
	vars, err := h.Source.Read()
	if err != nil {
		return nil, err
	}
	stacks, err := stackrecords.StackNames(ctx, vars.Records, edge.ClassPreview, slug)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var named []string
	for _, stack := range stacks {
		if stack.Env == "" || slices.Contains(named, stack.Env) {
			continue
		}
		named = append(named, stack.Env)
	}
	slices.Sort(named)
	return named, nil
}

func (h *VarsService) SetValue(ctx context.Context, req *envvarsv1.SetValueRequest) (*envvarsv1.SetValueResponse, error) {
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

func (h *VarsService) ListValues(ctx context.Context, req *envvarsv1.ListValuesRequest) (*envvarsv1.ListValuesResponse, error) {
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

func (h *VarsService) GetValue(ctx context.Context, req *envvarsv1.GetValueRequest) (*envvarsv1.GetValueResponse, error) {
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

func (h *VarsService) RevealValues(ctx context.Context, req *envvarsv1.RevealValuesRequest) (*envvarsv1.RevealValuesResponse, error) {
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

func (h *VarsService) DeleteValue(ctx context.Context, req *envvarsv1.DeleteValueRequest) (*envvarsv1.DeleteValueResponse, error) {
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

func (h *VarsService) SetReference(ctx context.Context, req *envvarsv1.SetReferenceRequest) (*envvarsv1.SetReferenceResponse, error) {
	if err := h.addressable(ctx, req.GetTier(), req.GetCoordinate()); err != nil {
		return nil, err
	}
	target := req.GetTarget()
	if target.GetEnvironment() != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"a reference resolves against the value %s sets for all environments; %q is an environment of the project holding the reference, and names nothing in the target's",
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

func (h *VarsService) ListReferences(ctx context.Context, req *envvarsv1.ListReferencesRequest) (*envvarsv1.ListReferencesResponse, error) {
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

func (h *VarsService) ListVersions(ctx context.Context, req *envvarsv1.ListVersionsRequest) (*envvarsv1.ListVersionsResponse, error) {
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

func (h *VarsService) SetBinding(ctx context.Context, req *envvarsv1.SetBindingRequest) (*envvarsv1.SetBindingResponse, error) {
	if err := bindingTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	binding := req.GetBinding()
	if err := envvars.ValidateBindingName(req.GetEnvironment(), binding.GetName()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := ValidatePublisher(req.GetOwner()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := ValidateInlineClaim(req.GetOwner(), binding.GetName()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := RefuseUnsourced(req.GetOwner(), binding); err != nil {
		return nil, bindingsError(err)
	}
	if err := VerifyBinding(binding); err != nil {
		return nil, bindingsError(err)
	}
	if err := h.verifyGrants(ctx, binding); err != nil {
		return nil, bindingsError(err)
	}
	pair, err := BindingPair(req.GetOwner(), binding)
	if err != nil {
		return nil, bindingsError(err)
	}

	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	version, err := store.SetBinding(ctx, scope, req.GetEnvironment(), req.GetOwner(), binding.GetName(), pair)
	if err != nil {
		return nil, bindingsError(err)
	}
	return &envvarsv1.SetBindingResponse{Version: uint64(version)}, nil
}

func (h *VarsService) RemoveBinding(ctx context.Context, req *envvarsv1.RemoveBindingRequest) (*envvarsv1.RemoveBindingResponse, error) {
	if err := bindingTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	if err := envvars.ValidateBindingName(req.GetEnvironment(), req.GetName()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	removed, err := store.RemoveBinding(ctx, scope, req.GetEnvironment(), req.GetName())
	if err != nil {
		return nil, bindingsError(err)
	}
	return &envvarsv1.RemoveBindingResponse{Removed: removed}, nil
}

func (h *VarsService) ListBindings(ctx context.Context, req *envvarsv1.ListBindingsRequest) (*envvarsv1.ListBindingsResponse, error) {
	if err := bindingTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	store, scope, err := h.scoped(req.GetTier(), req.GetSlug())
	if err != nil {
		return nil, err
	}
	found, err := store.ListBindings(ctx, scope, req.GetEnvironment())
	if err != nil {
		return nil, bindingsError(err)
	}
	resp := &envvarsv1.ListBindingsResponse{Bindings: make([]*envvarsv1.BindingSummary, 0, len(found))}
	for _, published := range found {
		binding, err := DecodeBinding(published.Record)
		if err != nil {
			return nil, bindingsError(fmt.Errorf("read binding %s's record: %w: %w", published.Name, err, ErrUnreadableRecord))
		}
		shapes, err := DecodeShapes(published.Shapes)
		if err != nil {
			return nil, bindingsError(fmt.Errorf("read binding %s's shape: %w: %w", published.Name, err, ErrUnreadableRecord))
		}
		resp.Bindings = append(resp.Bindings, &envvarsv1.BindingSummary{
			Name:       published.Name,
			Type:       naming.BindingTypeOf(binding),
			Source:     binding.GetSource(),
			Owner:      published.Owner,
			Version:    uint64(published.Version),
			Properties: naming.PropertyShapeMessages(shapes),
		})
	}
	return resp, nil
}

func bindingTarget(tier environmentv1.Tier, environment string) error {
	if environment != "" && tier != environmentv1.Tier_TIER_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"environment %q is named alongside class %q: an ocel coordinate is a class and, in %s, one preview environment; leave the environment off",
			environment, edge.ClassProduction, edge.ClassPreview))
	}
	return nil
}

func coordinateOf(c *envvarsv1.Coordinate) envvars.Coordinate {
	return envvars.Coordinate{
		Cell:        envvars.Cell{Folder: c.GetFolder(), Key: c.GetKey()},
		Environment: c.GetEnvironment(),
	}
}

func coordinateProto(slug string, c envvars.Coordinate) *envvarsv1.Coordinate {
	return &envvarsv1.Coordinate{
		Slug:        slug,
		Folder:      c.Folder,
		Key:         c.Key,
		Environment: c.Environment,
	}
}

func metadataProto(scope envvars.Scope, m envvars.Metadata) *envvarsv1.ValueMetadata {
	out := &envvarsv1.ValueMetadata{
		Coordinate: coordinateProto(scope.Project, m.Coordinate),
		Version:    m.Version,
		UpdatedAt:  m.UpdatedAt,
		Size:       m.Size,
	}
	if m.Target != nil {
		out.Target = &envvarsv1.Coordinate{
			Slug:   m.Target.Project,
			Folder: m.Target.Folder,
			Key:    m.Target.Key,
		}
	}
	return out
}

func valuesError(err error) error {
	switch {
	case errors.Is(err, envvars.ErrStaleVersion):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, envvars.ErrDangling):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, envvars.ErrWouldDeepen), errors.Is(err, envvars.ErrIsReference), errors.Is(err, envvars.ErrTooLarge):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, envvars.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return provider.RefusalError(err)
	}
}

func bindingsError(err error) error {
	switch {
	case errors.Is(err, envvars.ErrClaimed):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, envvars.ErrTornPair):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, ErrUnsourced), errors.Is(err, ErrUnreadableRecord),
		errors.Is(err, provider.ErrUnscopedGrant), errors.Is(err, ErrUnattachedGrant):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, envvars.ErrNotPublished):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return valuesError(err)
	}
}
