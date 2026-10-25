package variablestoreserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ocelhq/ocel/pkg/bindings"
	"github.com/ocelhq/ocel/pkg/environment"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

var (
	ErrUnsourced = errors.New("unsourced binding")

	ErrUnreadableRecord = errors.New("unreadable binding record")
)

func (h *Service) SetBinding(ctx context.Context, req *variablestorev1.SetBindingRequest) (*variablestorev1.SetBindingResponse, error) {
	if err := bindingTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	published := req.GetPublished()
	binding := published.GetBinding()
	if err := variablestore.ValidateBindingName(req.GetEnvironment(), binding.GetName()); err != nil {
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
	if err := VerifyBinding(published); err != nil {
		return nil, bindingsError(err)
	}
	pair, err := BindingPair(req.GetOwner(), published)
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
	return &variablestorev1.SetBindingResponse{Version: uint64(version)}, nil
}

func (h *Service) RemoveBinding(ctx context.Context, req *variablestorev1.RemoveBindingRequest) (*variablestorev1.RemoveBindingResponse, error) {
	if err := bindingTarget(req.GetTier(), req.GetEnvironment()); err != nil {
		return nil, err
	}
	if err := variablestore.ValidateBindingName(req.GetEnvironment(), req.GetName()); err != nil {
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
	return &variablestorev1.RemoveBindingResponse{Removed: removed}, nil
}

func (h *Service) ListBindings(ctx context.Context, req *variablestorev1.ListBindingsRequest) (*variablestorev1.ListBindingsResponse, error) {
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
	resp := &variablestorev1.ListBindingsResponse{Bindings: make([]*variablestorev1.BindingSummary, 0, len(found))}
	for _, published := range found {
		record, err := DecodePublishedBinding(published.Record)
		if err != nil {
			return nil, bindingsError(fmt.Errorf("read binding %s's record: %w: %w", published.Name, err, ErrUnreadableRecord))
		}
		shapes, err := DecodeShapes(published.Shapes)
		if err != nil {
			return nil, bindingsError(fmt.Errorf("read binding %s's shape: %w: %w", published.Name, err, ErrUnreadableRecord))
		}
		resp.Bindings = append(resp.Bindings, &variablestorev1.BindingSummary{
			Name:       published.Name,
			Type:       bindings.TypeOf(record.GetBinding()),
			Source:     record.GetBinding().GetSource(),
			Owner:      published.Owner,
			Version:    uint64(published.Version),
			Properties: bindings.PropertyShapeMessages(shapes),
		})
	}
	return resp, nil
}

func bindingTarget(tier environmentv1.Tier, env string) error {
	if env != "" && tier != environmentv1.Tier_TIER_PREVIEW {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"environment %q is named alongside tier %q: an ocel coordinate is a tier and, in %s, one preview environment; leave the environment off",
			env, environment.TierProduction, environment.TierPreview))
	}
	return nil
}

func bindingsError(err error) error {
	switch {
	case errors.Is(err, variablestore.ErrClaimed):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, variablestore.ErrTornPair):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, ErrUnsourced), errors.Is(err, ErrUnreadableRecord),
		errors.Is(err, provider.ErrUnscopedGrant):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, variablestore.ErrNotPublished):
		return connect.NewError(connect.CodeNotFound, err)
	default:
		return valuesError(err)
	}
}

func EncodePublishedBinding(published *variablestorev1.PublishedBinding) ([]byte, error) {
	return protojson.Marshal(published)
}

func DecodePublishedBinding(raw []byte) (*variablestorev1.PublishedBinding, error) {
	published := &variablestorev1.PublishedBinding{}
	if err := protojson.Unmarshal(raw, published); err != nil {
		return nil, err
	}
	return published, nil
}

func BindingPair(owner string, published *variablestorev1.PublishedBinding) (variablestore.BindingWrite, error) {
	name := published.GetBinding().GetName()
	value, err := EncodePublishedBinding(published)
	if err != nil {
		return variablestore.BindingWrite{}, fmt.Errorf("render binding %s: %w", name, err)
	}
	if len(value) > variablestore.MaxValueBytes {
		return variablestore.BindingWrite{}, refusal.Refuse(refusal.CodeInvalid, "binding %s is too large: %d bytes, limit %d", name, len(value), variablestore.MaxValueBytes)
	}
	record, err := EncodePublishedBinding(redacted(published))
	if err != nil {
		return variablestore.BindingWrite{}, fmt.Errorf("render binding %s's record: %w", name, err)
	}
	shapes, err := json.Marshal(bindings.PropertyShapes(published.GetBinding()))
	if err != nil {
		return variablestore.BindingWrite{}, fmt.Errorf("render binding %s's shape: %w", name, err)
	}
	return variablestore.BindingWrite{Record: record, Shapes: shapes, Value: value, Owner: owner}, nil
}

func redacted(published *variablestorev1.PublishedBinding) *variablestorev1.PublishedBinding {
	out := proto.Clone(published).(*variablestorev1.PublishedBinding)
	if out.Binding == nil {
		return out
	}
	m := out.Binding.ProtoReflect()
	if fd := m.WhichOneof(m.Descriptor().Oneofs().ByName("properties")); fd != nil {
		m.Set(fd, protoreflect.ValueOfMessage(m.Get(fd).Message().New()))
	}
	return out
}

func DecodeShapes(raw []byte) ([]bindings.PropertyShape, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var shapes []bindings.PropertyShape
	if err := json.Unmarshal(raw, &shapes); err != nil {
		return nil, err
	}
	return shapes, nil
}

func RefuseUnsourced(publisher string, binding *bindingsv1.Binding) error {
	if binding.GetSource() != "" {
		return nil
	}
	if bindings.TypeOf(binding) == bindingsv1.BindingType_BINDING_TYPE_CUSTOM {
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
	if err := variablestore.ValidateOwner(publisher); err != nil {
		return err
	}
	if publisher == variablestore.OwnerOcel {
		return fmt.Errorf("publisher name %q names ocel's own provisioning; every record it stamps would be one ocel's next deploy may prune", variablestore.OwnerOcel)
	}
	return nil
}

func ValidateInlineClaim(publisher, name string) error {
	reserved := bindings.IsInlineRecord(name)
	switch {
	case reserved && publisher != bindings.InlineRecordOwner:
		return fmt.Errorf("binding name %q starts %q, which names the record ocel keeps for a binding written inline in the config; publish yours under another name", name, bindings.InlineRecordPrefix)
	case !reserved && publisher == bindings.InlineRecordOwner:
		return fmt.Errorf("publisher name %q writes only the records inline bindings keep, named %q<type>.<name>", publisher, bindings.InlineRecordPrefix)
	}
	return nil
}

func VerifyBinding(published *variablestorev1.PublishedBinding) error {
	binding := published.GetBinding()
	if binding.GetName() == "" {
		return fmt.Errorf("a binding has no name; the name is what a consuming app binds to: %w", ErrUnreadableRecord)
	}
	if bindings.TypeOf(binding) == bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED {
		return fmt.Errorf("binding %s has no properties, so it has no type a consumer can resolve it against: %w", binding.GetName(), ErrUnreadableRecord)
	}
	if bindings.TypeOf(binding) == bindingsv1.BindingType_BINDING_TYPE_CUSTOM {
		if binding.GetSource() == "" {
			return unsourcedCustom(binding)
		}
	}
	return VerifyGrantScope(published)
}

const grantWildcard = "*"

func wildcardsAService(action string) bool {
	service, verb, scoped := strings.Cut(action, ":")
	return scoped && (service == grantWildcard || verb == grantWildcard)
}

func VerifyGrantScope(published *variablestorev1.PublishedBinding) error {
	binding := published.GetBinding()
	for _, g := range published.GetGrants() {
		var err error
		switch {
		case g.GetRole() != "" && (len(g.GetActions()) > 0 || len(g.GetResources()) > 0):
			err = fmt.Errorf("binding %s has a grant of role %s that also names actions %v: a grant is a role on one resource or actions over resources, never both: %w",
				binding.GetName(), g.GetRole(), g.GetActions(), provider.ErrUnscopedGrant)
		case g.GetRole() != "" || g.GetResource() != "":
			err = verifyRoleScope(binding.GetName(), g)
		default:
			err = verifyActionScope(binding.GetName(), g)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func verifyRoleScope(name string, g *variablestorev1.Grant) error {
	switch {
	case g.GetRole() == "":
		return fmt.Errorf("binding %s has a grant on %s naming no role: a grant names what an app may do with the resource it binds: %w",
			name, g.GetResource(), provider.ErrUnscopedGrant)
	case g.GetRole() == grantWildcard:
		return fmt.Errorf("binding %s grants %q on %s: %q is every role, which reaches past what an app needs of the resource: %w",
			name, grantWildcard, g.GetResource(), grantWildcard, provider.ErrUnscopedGrant)
	case g.GetResource() == "":
		return fmt.Errorf("binding %s grants role %s on no resource: an app receives permissions for the resource it binds and nothing else: %w",
			name, g.GetRole(), provider.ErrUnscopedGrant)
	case g.GetResource() == grantWildcard:
		return fmt.Errorf("binding %s grants role %s on %q: %q is every resource in the account, and an app receives permissions for the resource it binds and nothing else: %w",
			name, g.GetRole(), grantWildcard, grantWildcard, provider.ErrUnscopedGrant)
	}
	return nil
}

func verifyActionScope(name string, g *variablestorev1.Grant) error {
	switch {
	case len(g.GetActions()) == 0:
		return fmt.Errorf("binding %s has a grant over %v naming no action: a grant names what an app may do with the resource it binds: %w",
			name, g.GetResources(), provider.ErrUnscopedGrant)
	case len(g.GetResources()) == 0:
		return fmt.Errorf("binding %s grants %v over no resource: an app receives permissions for the resource it binds and nothing else: %w",
			name, g.GetActions(), provider.ErrUnscopedGrant)
	case slices.Contains(g.GetActions(), grantWildcard):
		return fmt.Errorf("binding %s grants %q over %v: %q is every action any vendor has, which reaches past the resource it binds: %w",
			name, grantWildcard, g.GetResources(), grantWildcard, provider.ErrUnscopedGrant)
	case slices.ContainsFunc(g.GetActions(), wildcardsAService):
		at := slices.IndexFunc(g.GetActions(), wildcardsAService)
		return fmt.Errorf("binding %s grants %q over %v: a wildcard on either side of a service's action is every action of that service, or that action on every service: %w",
			name, g.GetActions()[at], g.GetResources(), provider.ErrUnscopedGrant)
	case slices.Contains(g.GetResources(), grantWildcard):
		return fmt.Errorf("binding %s grants %v over %q: %q is every resource in the account, and an app receives permissions for the resource it binds and nothing else: %w",
			name, g.GetActions(), grantWildcard, grantWildcard, provider.ErrUnscopedGrant)
	}
	return nil
}
