package providerkit

import (
	"strconv"

	"google.golang.org/protobuf/types/known/structpb"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type Resource struct {
	Name string
	Type LinkType

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
	message := &linksv1.Link{Name: link.Name}
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

var linkTypes = map[linksv1.LinkType]LinkType{
	linksv1.LinkType_LINK_TYPE_POSTGRES: LinkPostgres,
	linksv1.LinkType_LINK_TYPE_BUCKET:   LinkBucket,
	linksv1.LinkType_LINK_TYPE_CUSTOM:   LinkCustom,
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
	name := held.GetResource().GetName()
	if name == "" {
		name = held.GetLogicalName()
	}
	if name == "" {
		return Resource{}, Refuse(CodeInvalid, "this manifest declares a resource with no name, and a link is bound by name")
	}
	kind, known := linkTypes[held.GetResource().GetType()]
	if !known {
		return Resource{}, Refuse(CodeInvalid, "resource %s declares no type, so nothing knows what to stand up for it", name)
	}
	resource := Resource{Name: name, Type: kind}
	switch {
	case held.GetPostgres() != nil:
		resource.Postgres = &PostgresSpec{Version: held.GetPostgres().GetVersion()}
	case held.GetBucket() != nil:
		resource.Bucket = &BucketSpec{AllowedOrigins: held.GetBucket().GetAllowedOrigins()}
	}
	return resource, nil
}
