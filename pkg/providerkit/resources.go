package providerkit

import (
	"strconv"

	"google.golang.org/protobuf/types/known/structpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Resource struct {
	Name     string
	Declared string
	Type     BindingType
	Binding  string

	Postgres  *PostgresSpec
	Bucket    *BucketSpec
	Container *ContainerSpec
	Custom    *CustomSpec
}

type PostgresSpec struct {
	Version string
}

type BucketSpec struct {
	AllowedOrigins []string
}

type ContainerSpec struct {
	Image string
	Port  int
	Env   map[string]string
}

type CustomSpec struct {
	Type   string
	Config map[string]any
}

const (
	BindingPostgres  BindingType = "postgres"
	BindingBucket    BindingType = "bucket"
	BindingContainer BindingType = "container"
	BindingCustom    BindingType = "custom"
)

const (
	PropertyHost     = "host"
	PropertyPort     = "port"
	PropertyDatabase = "database"
	PropertyUsername = "username"
	PropertyPassword = "password"
	PropertyBucket   = "bucket"
)

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
		return Refuse(CodeInvalid, "a %s binding came back with no name, and a consuming app binds to the name", binding.Type)
	}
	for _, name := range RequiredProperties(binding.Type) {
		if binding.Properties[name] == "" {
			return Refuse(CodeInvalid,
				"binding %s came back without %q: every %s binding carries %v, and an app binds a client to the whole set",
				binding.Name, name, binding.Type, RequiredProperties(binding.Type))
		}
	}
	if binding.Type == BindingPostgres {
		if _, err := strconv.Atoi(binding.Properties[PropertyPort]); err != nil {
			return Refuse(CodeInvalid, "binding %s came back with port %q, which is not a port number",
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
			Bucket: binding.Properties[PropertyBucket],
		}}
	default:
		fields := make(map[string]any, len(binding.Properties))
		for name, value := range binding.Properties {
			fields[name] = value
		}
		custom, err := structpb.NewStruct(fields)
		if err != nil {
			return nil, Refuse(CodeInvalid, "binding %s carries properties no record can hold: %v", binding.Name, err)
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

var resourceTypes = map[resourcesv1.ResourceType]BindingType{
	resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES:  BindingPostgres,
	resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:    BindingBucket,
	resourcesv1.ResourceType_RESOURCE_TYPE_CONTAINER: BindingContainer,
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

func manifestResources(manifest *contractv1.Manifest) ([]Resource, error) {
	declared := manifest.GetResources()
	resources := make([]Resource, 0, len(declared))
	for _, held := range declared {
		resource, err := manifestResource(held)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func manifestResource(held *contractv1.ManifestResource) (Resource, error) {
	name := held.GetLogicalName()
	declared := held.GetResource().GetName()
	if name == "" {
		name = declared
	}
	if declared == "" {
		declared = name
	}
	if name == "" {
		return Resource{}, Refuse(CodeInvalid, "this manifest declares a resource with no name, and a binding is bound by name")
	}
	kind, known := resourceTypes[held.GetResource().GetType()]
	if !known {
		return Resource{}, Refuse(CodeInvalid, "resource %s declares no type, so nothing knows what to stand up for it", name)
	}
	resource := Resource{Name: name, Declared: declared, Type: kind, Binding: held.GetBinding()}
	switch {
	case held.GetPostgres() != nil:
		resource.Postgres = &PostgresSpec{Version: held.GetPostgres().GetVersion()}
	case held.GetBucket() != nil:
		resource.Bucket = &BucketSpec{AllowedOrigins: held.GetBucket().GetAllowedOrigins()}
	}
	return resource, nil
}
