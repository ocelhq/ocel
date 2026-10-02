package providerserver

import (
	"github.com/ocelhq/ocel/pkg/kvstore"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

var resourceTypes = map[resourcesv1.ResourceType]provider.BindingType{
	resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES: provider.BindingPostgres,
	resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET:   provider.BindingBucket,
	resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC:    provider.BindingTopic,
	resourcesv1.ResourceType_RESOURCE_TYPE_TASK:     provider.BindingTask,
	resourcesv1.ResourceType_RESOURCE_TYPE_KV:       provider.BindingKV,
	resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME: provider.BindingRealtime,
}

func manifestResources(manifest *contractv1.Manifest) ([]provider.Resource, error) {
	declared := manifest.GetResources()
	resources := make([]provider.Resource, 0, len(declared))
	for _, resource := range declared {
		resource, err := manifestResource(resource)
		if err != nil {
			return nil, err
		}
		if err := refuseConsumerOnUndeclaredWorker(manifest, resource); err != nil {
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
	if message.GetTopic() != nil && kind != provider.BindingTopic && kind != provider.BindingTask {
		return provider.Resource{}, refusal.Refuse(refusal.CodeInvalid, "%s %s carries a topic's config, and only a topic or a task takes one", kind, declared)
	}
	if message.GetRealtime() != nil && kind != provider.BindingRealtime {
		return provider.Resource{}, refusal.Refuse(refusal.CodeInvalid, "%s %s carries a realtime config, and only realtime takes one", kind, declared)
	}
	switch {
	case kind == provider.BindingRealtime:
		spec, err := readRealtimeSpec(declared, message.GetRealtime())
		if err != nil {
			return provider.Resource{}, refusal.Refuse(refusal.CodeInvalid, "realtime %s: %s", declared, err)
		}
		resource.Realtime = spec
	case kind == provider.BindingTopic || kind == provider.BindingTask:
		spec, err := readTopicSpec(kind, message.GetTopic())
		if err != nil {
			return provider.Resource{}, refusal.Refuse(refusal.CodeInvalid, "%s %s: %s", kind, declared, err)
		}
		resource.Topic = spec
	case message.GetPostgres() != nil:
		resource.Postgres = &provider.PostgresSpec{Version: message.GetPostgres().GetVersion()}
	case message.GetBucket() != nil:
		resource.Bucket = &provider.BucketSpec{AllowedOrigins: message.GetBucket().GetAllowedOrigins(), Public: message.GetBucket().GetPublic()}
	case message.GetKv() != nil:
		spec, err := kvSpec(message.GetKv())
		if err != nil {
			return provider.Resource{}, refusal.Refuse(refusal.CodeInvalid, "kv store %s: %s", declared, err)
		}
		resource.KV = spec
	}
	return resource, nil
}

func kvSpec(config *resourcesv1.KvConfig) (*provider.KVSpec, error) {
	settings, err := kvstore.ReadSettings(config)
	if err != nil {
		return nil, err
	}
	return &provider.KVSpec{Version: settings.Version, Eviction: settings.Eviction, MemoryBytes: settings.MemoryBytes}, nil
}
