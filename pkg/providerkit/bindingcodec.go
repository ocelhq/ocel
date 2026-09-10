package providerkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

var (
	ErrUnsourced = errors.New("providerkit: unsourced binding")

	ErrUnreadableRecord = errors.New("providerkit: unreadable binding record")

	ErrUnscopedGrant = errors.New("providerkit: unscoped grant")

	ErrUnattachedGrant = errors.New("providerkit: unattached grant")
)

func EncodeBinding(binding *bindingsv1.Binding) ([]byte, error) { return protojson.Marshal(binding) }

func DecodeBinding(raw []byte) (*bindingsv1.Binding, error) {
	binding := &bindingsv1.Binding{}
	if err := protojson.Unmarshal(raw, binding); err != nil {
		return nil, err
	}
	return binding, nil
}

func BindingPair(owner string, binding *bindingsv1.Binding) (values.Pair, error) {
	value, err := EncodeBinding(binding)
	if err != nil {
		return values.Pair{}, fmt.Errorf("render binding %s: %w", binding.GetName(), err)
	}
	if len(value) > values.MaxValueBytes {
		return values.Pair{}, Refuse(CodeInvalid, "binding %s is too large: %d bytes, limit %d", binding.GetName(), len(value), values.MaxValueBytes)
	}
	record, err := EncodeBinding(redacted(binding))
	if err != nil {
		return values.Pair{}, fmt.Errorf("render binding %s's record: %w", binding.GetName(), err)
	}
	shapes, err := json.Marshal(naming.BindingPropertyShapes(binding))
	if err != nil {
		return values.Pair{}, fmt.Errorf("render binding %s's shape: %w", binding.GetName(), err)
	}
	return values.Pair{Record: record, Shapes: shapes, Value: value, Owner: owner}, nil
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
	if err := values.ValidateOwner(publisher); err != nil {
		return err
	}
	if publisher == values.OwnerOcel {
		return fmt.Errorf("publisher name %q names ocel's own provisioning; every record it stamps would be one ocel's next deploy may prune", values.OwnerOcel)
	}
	return nil
}

func VerifyBinding(binding *bindingsv1.Binding) error {
	if binding.GetName() == "" {
		return fmt.Errorf("a binding carries no name; the name is what a consuming app binds to: %w", ErrUnreadableRecord)
	}
	if naming.BindingTypeOf(binding) == bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED {
		return fmt.Errorf("binding %s carries no properties, so it has no type a consumer can resolve it against: %w", binding.GetName(), ErrUnreadableRecord)
	}
	if naming.BindingTypeOf(binding) == bindingsv1.BindingType_BINDING_TYPE_CUSTOM {
		if binding.GetSource() == "" {
			return unsourcedCustom(binding)
		}
		if len(binding.GetGrants()) > 0 {
			return fmt.Errorf(
				"binding %s is a custom record carrying %d grants: no consumer attaches a custom binding's grants yet; a grant nobody attaches is a permission the record claims and no app holds. "+
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
			return fmt.Errorf("binding %s carries a grant over %v naming no action: a grant names what an app may do with the resource it binds: %w",
				binding.GetName(), g.GetResources(), ErrUnscopedGrant)
		}
		if len(g.GetResources()) == 0 {
			return fmt.Errorf("binding %s grants %v over no resource: an app receives permissions for the resource it binds and nothing else: %w",
				binding.GetName(), g.GetActions(), ErrUnscopedGrant)
		}
		if slices.Contains(g.GetActions(), grantWildcard) {
			return fmt.Errorf("binding %s grants %q over %v: %q is every action any vendor has, which reaches past the resource it binds: %w",
				binding.GetName(), grantWildcard, g.GetResources(), grantWildcard, ErrUnscopedGrant)
		}
		if slices.Contains(g.GetResources(), grantWildcard) {
			return fmt.Errorf("binding %s grants %v over %q: %q is every resource in the account, and an app receives permissions for the resource it binds and nothing else: %w",
				binding.GetName(), g.GetActions(), grantWildcard, grantWildcard, ErrUnscopedGrant)
		}
	}
	return nil
}
