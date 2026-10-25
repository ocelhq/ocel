package provider

import (
	"github.com/ocelhq/ocel/pkg/bindings"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/live"
)

func (v AppValues) LiveKeys() []live.Key {
	keys := make([]live.Key, 0, len(v.Secrets))
	for _, secret := range v.Secrets {
		if secret.Binding != "" {
			continue
		}
		keys = append(keys, live.Key{Key: secret.Key, Folder: secret.Folder})
	}
	return keys
}

func (v AppValues) LiveBindings() []live.Binding {
	declared := make([]live.Binding, 0, len(v.Bindings)+len(v.Secrets))
	for _, binding := range v.Bindings {
		kind := ProtoBindingType(binding.Type)
		resource := binding.Resource
		if resource == "" {
			resource = binding.Name
		}
		declared = append(declared, live.Binding{
			Name:    binding.Name,
			Key:     bindings.ResourceEnvName(kind, resource),
			Type:    kind,
			Granted: binding.Version,
		})
	}
	for _, secret := range v.Secrets {
		if secret.Binding == "" {
			continue
		}
		declared = append(declared, live.Binding{
			Name:     secret.Binding,
			Key:      secret.Key,
			Type:     bindingsv1.BindingType_BINDING_TYPE_CUSTOM,
			Property: secret.Property,
		})
	}
	return declared
}
