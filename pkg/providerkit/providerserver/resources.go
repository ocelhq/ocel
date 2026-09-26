package providerserver

import (
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

var resourceTypes = map[resourcesv1.ResourceType]provider.BindingType{
	resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES:  provider.BindingPostgres,
	resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:    provider.BindingBucket,
	resourcesv1.ResourceType_RESOURCE_TYPE_CONTAINER: provider.BindingContainer,
}

func manifestResources(manifest *contractv1.Manifest) ([]provider.Resource, error) {
	declared := manifest.GetResources()
	resources := make([]provider.Resource, 0, len(declared))
	for _, resource := range declared {
		resource, err := manifestResource(resource)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	return resources, nil
}

func manifestResource(message *contractv1.ManifestResource) (provider.Resource, error) {
	name := message.GetLogicalName()
	declared := message.GetResource().GetName()
	if name == "" {
		name = declared
	}
	if declared == "" {
		declared = name
	}
	if name == "" {
		return provider.Resource{}, refusal.Refuse(refusal.CodeInvalid, "this manifest declares a resource with no name, and a binding is bound by name")
	}
	kind, known := resourceTypes[message.GetResource().GetType()]
	if !known {
		return provider.Resource{}, refusal.Refuse(refusal.CodeInvalid, "resource %s declares no type, so nothing knows what to provision for it", name)
	}
	resource := provider.Resource{Name: name, Declared: declared, Type: kind, Binding: message.GetBinding()}
	switch {
	case message.GetPostgres() != nil:
		resource.Postgres = &provider.PostgresSpec{Version: message.GetPostgres().GetVersion()}
	case message.GetBucket() != nil:
		resource.Bucket = &provider.BucketSpec{AllowedOrigins: message.GetBucket().GetAllowedOrigins(), Public: message.GetBucket().GetPublic()}
	}
	return resource, nil
}
