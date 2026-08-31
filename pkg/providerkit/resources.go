package providerkit

import (
	"strconv"

	"google.golang.org/protobuf/types/known/structpb"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Resource struct {
	Name     string
	Declared string
	Type     LinkType
	Linked   bool

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
	LinkPostgres  LinkType = "postgres"
	LinkBucket    LinkType = "bucket"
	LinkContainer LinkType = "container"
	LinkCustom    LinkType = "custom"
)

const (
	PropertyHost     = "host"
	PropertyPort     = "port"
	PropertyDatabase = "database"
	PropertyUsername = "username"
	PropertyPassword = "password"
	PropertyBucket   = "bucket"
)

func RequiredProperties(t LinkType) []string {
	switch t {
	case LinkPostgres:
		return []string{PropertyHost, PropertyPort, PropertyDatabase, PropertyUsername, PropertyPassword}
	case LinkBucket:
		return []string{PropertyBucket}
	}
	return nil
}

func VerifyProperties(link Link) error {
	if link.Name == "" {
		return Refuse(CodeInvalid, "a %s link came back with no name, and a consuming app binds to the name", link.Type)
	}
	for _, name := range RequiredProperties(link.Type) {
		if link.Properties[name] == "" {
			return Refuse(CodeInvalid,
				"link %s came back without %q: every %s link carries %v, and an app binds a client to the whole set",
				link.Name, name, link.Type, RequiredProperties(link.Type))
		}
	}
	if link.Type == LinkPostgres {
		if _, err := strconv.Atoi(link.Properties[PropertyPort]); err != nil {
			return Refuse(CodeInvalid, "link %s came back with port %q, which is not a port number",
				link.Name, link.Properties[PropertyPort])
		}
	}
	return nil
}

func LinkMessage(link Link) (*linksv1.Link, error) {
	if err := VerifyProperties(link); err != nil {
		return nil, err
	}
	message := &linksv1.Link{Name: link.Name, Grants: grantMessages(link.Grants)}
	switch link.Type {
	case LinkPostgres:
		port, _ := strconv.Atoi(link.Properties[PropertyPort])
		message.Properties = &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
			Host:     link.Properties[PropertyHost],
			Port:     int32(port),
			Database: link.Properties[PropertyDatabase],
			Username: link.Properties[PropertyUsername],
			Password: link.Properties[PropertyPassword],
		}}
	case LinkBucket:
		message.Properties = &linksv1.Link_Bucket{Bucket: &linksv1.BucketProperties{
			Bucket: link.Properties[PropertyBucket],
		}}
	default:
		fields := make(map[string]any, len(link.Properties))
		for name, value := range link.Properties {
			fields[name] = value
		}
		custom, err := structpb.NewStruct(fields)
		if err != nil {
			return nil, Refuse(CodeInvalid, "link %s carries properties no record can hold: %v", link.Name, err)
		}
		message.Properties = &linksv1.Link_Custom{Custom: custom}
	}
	return message, nil
}

func grantMessages(grants []Grant) []*linksv1.Grant {
	if len(grants) == 0 {
		return nil
	}
	out := make([]*linksv1.Grant, 0, len(grants))
	for _, grant := range grants {
		message := &linksv1.Grant{Label: grant.Label, Actions: grant.Actions, Resources: grant.Resources}
		for _, condition := range grant.Conditions {
			message.Conditions = append(message.Conditions, &linksv1.GrantCondition{
				Operator: condition.Operator,
				Key:      condition.Key,
				Values:   condition.Values,
			})
		}
		out = append(out, message)
	}
	return out
}

func GrantsOf(message *linksv1.Link) []Grant {
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

var linkTypes = map[linksv1.LinkType]LinkType{
	linksv1.LinkType_LINK_TYPE_POSTGRES: LinkPostgres,
	linksv1.LinkType_LINK_TYPE_BUCKET:   LinkBucket,
	linksv1.LinkType_LINK_TYPE_CUSTOM:   LinkCustom,
}

func WireLinkType(kind LinkType) linksv1.LinkType {
	switch kind {
	case LinkPostgres:
		return linksv1.LinkType_LINK_TYPE_POSTGRES
	case LinkBucket:
		return linksv1.LinkType_LINK_TYPE_BUCKET
	case LinkCustom:
		return linksv1.LinkType_LINK_TYPE_CUSTOM
	default:
		return linksv1.LinkType_LINK_TYPE_CUSTOM
	}
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
		return Resource{}, Refuse(CodeInvalid, "this manifest declares a resource with no name, and a link is bound by name")
	}
	kind, known := linkTypes[held.GetResource().GetType()]
	if !known {
		return Resource{}, Refuse(CodeInvalid, "resource %s declares no type, so nothing knows what to stand up for it", name)
	}
	resource := Resource{Name: name, Declared: declared, Type: kind, Linked: held.GetLinked()}
	switch {
	case held.GetPostgres() != nil:
		resource.Postgres = &PostgresSpec{Version: held.GetPostgres().GetVersion()}
	case held.GetBucket() != nil:
		resource.Bucket = &BucketSpec{AllowedOrigins: held.GetBucket().GetAllowedOrigins()}
	}
	return resource, nil
}
