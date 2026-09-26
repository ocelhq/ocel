package envvarsserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var (
	ErrUnsourced = errors.New("unsourced binding")

	ErrUnreadableRecord = errors.New("unreadable binding record")

	ErrUnattachedGrant = errors.New("unattached grant")
)

func (h *Service) SetBinding(ctx context.Context, req *envvarsv1.SetBindingRequest) (*envvarsv1.SetBindingResponse, error) {
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

func (h *Service) RemoveBinding(ctx context.Context, req *envvarsv1.RemoveBindingRequest) (*envvarsv1.RemoveBindingResponse, error) {
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

func (h *Service) ListBindings(ctx context.Context, req *envvarsv1.ListBindingsRequest) (*envvarsv1.ListBindingsResponse, error) {
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

func (h *Service) verifyGrants(ctx context.Context, binding *bindingsv1.Binding) error {
	if len(binding.GetGrants()) == 0 {
		return nil
	}
	backend, err := h.Source.Read()
	if err != nil {
		return err
	}
	if backend.VerifyGrants == nil {
		return nil
	}
	return backend.VerifyGrants(ctx, provider.BindingOf(binding))
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

func EncodeBinding(binding *bindingsv1.Binding) ([]byte, error) { return protojson.Marshal(binding) }

func DecodeBinding(raw []byte) (*bindingsv1.Binding, error) {
	binding := &bindingsv1.Binding{}
	if err := protojson.Unmarshal(raw, binding); err != nil {
		return nil, err
	}
	return binding, nil
}

func BindingPair(owner string, binding *bindingsv1.Binding) (envvars.BindingWrite, error) {
	value, err := EncodeBinding(binding)
	if err != nil {
		return envvars.BindingWrite{}, fmt.Errorf("render binding %s: %w", binding.GetName(), err)
	}
	if len(value) > envvars.MaxValueBytes {
		return envvars.BindingWrite{}, refusal.Refuse(refusal.CodeInvalid, "binding %s is too large: %d bytes, limit %d", binding.GetName(), len(value), envvars.MaxValueBytes)
	}
	record, err := EncodeBinding(redacted(binding))
	if err != nil {
		return envvars.BindingWrite{}, fmt.Errorf("render binding %s's record: %w", binding.GetName(), err)
	}
	shapes, err := json.Marshal(naming.BindingPropertyShapes(binding))
	if err != nil {
		return envvars.BindingWrite{}, fmt.Errorf("render binding %s's shape: %w", binding.GetName(), err)
	}
	return envvars.BindingWrite{Record: record, Shapes: shapes, Value: value, Owner: owner}, nil
}

func redacted(binding *bindingsv1.Binding) *bindingsv1.Binding {
	out := proto.Clone(binding).(*bindingsv1.Binding)
	m := out.ProtoReflect()
	if fd := m.WhichOneof(m.Descriptor().Oneofs().ByName("properties")); fd != nil {
		m.Set(fd, protoreflect.ValueOfMessage(m.Get(fd).Message().New()))
	}
	return out
}

func DecodeShapes(raw []byte) ([]naming.PropertyShape, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var shapes []naming.PropertyShape
	if err := json.Unmarshal(raw, &shapes); err != nil {
		return nil, err
	}
	return shapes, nil
}

func RefuseUnsourced(publisher string, binding *bindingsv1.Binding) error {
	if binding.GetSource() != "" {
		return nil
	}
	if naming.BindingTypeOf(binding) == bindingsv1.BindingType_BINDING_TYPE_CUSTOM {
		return unsourcedCustom(binding)
	}
	return fmt.Errorf(
		"publisher %s leaves binding %s unsourced: an empty source names what ocel's own provisioning produces, and an app binds a client to it on that promise. "+
			"Name the tool that publishes it: %w",
		publisher, binding.GetName(), ErrUnsourced)
}

func unsourcedCustom(binding *bindingsv1.Binding) error {
	return fmt.Errorf(
		"binding %s is a custom record with no source: only your own infrastructure publishes a custom binding; ocel provisions nothing it cannot type. "+
			"Name the tool that publishes it: %w",
		binding.GetName(), ErrUnsourced)
}

func ValidatePublisher(publisher string) error {
	if err := envvars.ValidateOwner(publisher); err != nil {
		return err
	}
	if publisher == envvars.OwnerOcel {
		return fmt.Errorf("publisher name %q names ocel's own provisioning; every record it stamps would be one ocel's next deploy may prune", envvars.OwnerOcel)
	}
	return nil
}

func ValidateInlineClaim(publisher, name string) error {
	reserved := naming.IsInlineRecord(name)
	switch {
	case reserved && publisher != naming.InlineRecordOwner:
		return fmt.Errorf("binding name %q starts %q, which names the record ocel keeps for a binding written inline in the config; publish yours under another name", name, naming.InlineRecordPrefix)
	case !reserved && publisher == naming.InlineRecordOwner:
		return fmt.Errorf("publisher name %q writes only the records inline bindings keep, named %q<type>.<name>", publisher, naming.InlineRecordPrefix)
	}
	return nil
}

func VerifyBinding(binding *bindingsv1.Binding) error {
	if binding.GetName() == "" {
		return fmt.Errorf("a binding has no name; the name is what a consuming app binds to: %w", ErrUnreadableRecord)
	}
	if naming.BindingTypeOf(binding) == bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED {
		return fmt.Errorf("binding %s has no properties, so it has no type a consumer can resolve it against: %w", binding.GetName(), ErrUnreadableRecord)
	}
	if naming.BindingTypeOf(binding) == bindingsv1.BindingType_BINDING_TYPE_CUSTOM {
		if binding.GetSource() == "" {
			return unsourcedCustom(binding)
		}
		if len(binding.GetGrants()) > 0 {
			return fmt.Errorf(
				"binding %s is a custom record with %d grants: no consumer attaches a custom binding's grants yet; a grant nobody attaches is a permission the record claims and no app has. "+
					"Publish it without them: %w",
				binding.GetName(), len(binding.GetGrants()), ErrUnattachedGrant)
		}
	}
	return VerifyGrantScope(binding)
}

const grantWildcard = "*"

func VerifyGrantScope(binding *bindingsv1.Binding) error {
	for _, g := range binding.GetGrants() {
		if len(g.GetActions()) == 0 {
			return fmt.Errorf("binding %s has a grant over %v naming no action: a grant names what an app may do with the resource it binds: %w",
				binding.GetName(), g.GetResources(), provider.ErrUnscopedGrant)
		}
		if len(g.GetResources()) == 0 {
			return fmt.Errorf("binding %s grants %v over no resource: an app receives permissions for the resource it binds and nothing else: %w",
				binding.GetName(), g.GetActions(), provider.ErrUnscopedGrant)
		}
		if slices.Contains(g.GetActions(), grantWildcard) {
			return fmt.Errorf("binding %s grants %q over %v: %q is every action any vendor has, which reaches past the resource it binds: %w",
				binding.GetName(), grantWildcard, g.GetResources(), grantWildcard, provider.ErrUnscopedGrant)
		}
		if slices.Contains(g.GetResources(), grantWildcard) {
			return fmt.Errorf("binding %s grants %v over %q: %q is every resource in the account, and an app receives permissions for the resource it binds and nothing else: %w",
				binding.GetName(), g.GetActions(), grantWildcard, grantWildcard, provider.ErrUnscopedGrant)
		}
	}
	return nil
}
