package provider

import (
	"fmt"
	"strconv"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

type Resource struct {
	Name     string
	Declared string
	Type     BindingType
	Binding  string

	Postgres  *PostgresSpec
	Bucket    *BucketSpec
	Container *ContainerSpec
}

type PostgresSpec struct {
	Version string
}

type BucketSpec struct {
	AllowedOrigins []string
	Public         bool
}

type ContainerSpec struct {
	Image string
	Port  int
	Env   map[string]string
}

const (
	BindingPostgres  BindingType = "postgres"
	BindingBucket    BindingType = "bucket"
	BindingContainer BindingType = "container"
	BindingCustom    BindingType = "custom"
)

const (
	PropertyHost          = "host"
	PropertyPort          = "port"
	PropertyDatabase      = "database"
	PropertyUsername      = "username"
	PropertyPassword      = "password"
	PropertyBucket        = "bucket"
	PropertyPublicBaseURL = "publicBaseUrl"
	PropertyPublic        = "public"
	PropertyEndpoint      = "endpoint"
)

func (b Binding) Endpointed() bool {
	return b.Type == BindingBucket && b.Properties[PropertyEndpoint] != ""
}

func RequiredProperties(t BindingType) []string {
	switch t {
	case BindingPostgres:
		return []string{PropertyHost, PropertyPort, PropertyDatabase, PropertyUsername, PropertyPassword}
	case BindingBucket:
		return []string{PropertyBucket}
	}
	return nil
}

func VerifyProperties(binding Binding) error {
	if binding.Name == "" {
		return refusal.Refuse(refusal.CodeInvalid, "a %s binding came back with no name, and a consuming app binds to the name", binding.Type)
	}
	for _, name := range RequiredProperties(binding.Type) {
		if binding.Properties[name] == "" {
			return refusal.Refuse(refusal.CodeInvalid,
				"binding %s came back without %q: every %s binding carries %v, and an app binds a client to the whole set",
				binding.Name, name, binding.Type, RequiredProperties(binding.Type))
		}
	}
	if binding.Type == BindingPostgres {
		if _, err := strconv.Atoi(binding.Properties[PropertyPort]); err != nil {
			return refusal.Refuse(refusal.CodeInvalid, "binding %s came back with port %q, which is not a port number",
				binding.Name, binding.Properties[PropertyPort])
		}
	}
	return nil
}

func BindingMessage(binding Binding) (*bindingsv1.Binding, error) {
	if err := VerifyProperties(binding); err != nil {
		return nil, err
	}
	message := &bindingsv1.Binding{Name: binding.Name, Grants: grantMessages(binding.Grants)}
	switch binding.Type {
	case BindingPostgres:
		port, _ := strconv.Atoi(binding.Properties[PropertyPort])
		message.Properties = &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
			Host:     binding.Properties[PropertyHost],
			Port:     int32(port),
			Database: binding.Properties[PropertyDatabase],
			Username: binding.Properties[PropertyUsername],
			Password: binding.Properties[PropertyPassword],
		}}
	case BindingBucket:
		message.Properties = &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{
			Bucket:        binding.Properties[PropertyBucket],
			PublicBaseUrl: binding.Properties[PropertyPublicBaseURL],
			Public:        binding.Properties[PropertyPublic] == "true",
		}}
	default:
		fields := make(map[string]any, len(binding.Properties))
		for name, value := range binding.Properties {
			fields[name] = value
		}
		custom, err := structpb.NewStruct(fields)
		if err != nil {
			return nil, refusal.Refuse(refusal.CodeInvalid, "binding %s carries properties no record can hold: %v", binding.Name, err)
		}
		message.Properties = &bindingsv1.Binding_Custom{Custom: custom}
	}
	return message, nil
}

func grantMessages(grants []Grant) []*bindingsv1.Grant {
	if len(grants) == 0 {
		return nil
	}
	out := make([]*bindingsv1.Grant, 0, len(grants))
	for _, grant := range grants {
		message := &bindingsv1.Grant{Label: grant.Label, Actions: grant.Actions, Resources: grant.Resources}
		for _, condition := range grant.Conditions {
			message.Conditions = append(message.Conditions, &bindingsv1.GrantCondition{
				Operator: condition.Operator,
				Key:      condition.Key,
				Values:   condition.Values,
			})
		}
		out = append(out, message)
	}
	return out
}

func BindingOf(message *bindingsv1.Binding) Binding {
	binding := Binding{
		Type:       BindingCustom,
		Name:       message.GetName(),
		Source:     message.GetSource(),
		Properties: map[string]string{},
		Grants:     GrantsOf(message),
	}
	if kind, known := BindingTypeFromWire(naming.BindingTypeOf(message)); known {
		binding.Type = kind
	}
	for _, name := range naming.BindingPropertyNames(message) {
		if value, held := naming.BindingProperty(message, name); held {
			binding.Properties[name] = fmt.Sprint(value)
		}
	}
	return binding
}

func GrantsOf(message *bindingsv1.Binding) []Grant {
	held := message.GetGrants()
	if len(held) == 0 {
		return nil
	}
	out := make([]Grant, 0, len(held))
	for _, grant := range held {
		carried := Grant{Label: grant.GetLabel(), Actions: grant.GetActions(), Resources: grant.GetResources()}
		for _, condition := range grant.GetConditions() {
			carried.Conditions = append(carried.Conditions, GrantCondition{
				Operator: condition.GetOperator(),
				Key:      condition.GetKey(),
				Values:   condition.GetValues(),
			})
		}
		out = append(out, carried)
	}
	return out
}

var bindingTypes = map[bindingsv1.BindingType]BindingType{
	bindingsv1.BindingType_BINDING_TYPE_POSTGRES: BindingPostgres,
	bindingsv1.BindingType_BINDING_TYPE_BUCKET:   BindingBucket,
	bindingsv1.BindingType_BINDING_TYPE_CUSTOM:   BindingCustom,
}

var wireBindingTypes = func() map[BindingType]bindingsv1.BindingType {
	out := make(map[BindingType]bindingsv1.BindingType, len(bindingTypes))
	for wire, kind := range bindingTypes {
		out[kind] = wire
	}
	return out
}()

func WireBindingType(kind BindingType) bindingsv1.BindingType {
	if wire, known := wireBindingTypes[kind]; known {
		return wire
	}
	return bindingsv1.BindingType_BINDING_TYPE_CUSTOM
}

func BindingTypeFromWire(wire bindingsv1.BindingType) (BindingType, bool) {
	kind, known := bindingTypes[wire]
	return kind, known
}

func ResourceEnvName(kind BindingType, resource string) string {
	return naming.ResourceEnvName(WireBindingType(kind), resource)
}
