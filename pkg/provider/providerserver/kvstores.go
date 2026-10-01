package providerserver

import (
	"slices"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RefuseUnsupportedKVStores(facts provider.Facts, manifest *contractv1.Manifest) error {
	if slices.Contains(facts.Bindings, provider.BindingKV) {
		return nil
	}
	for _, resource := range manifest.GetResources() {
		if resource.GetResource().GetType() == resourcesv1.ResourceType_RESOURCE_TYPE_KV {
			return refusal.Refuse(refusal.CodeUnsupported,
				"this project declares kv %q, and kv stores are unsupported on %s: it provisions none",
				resource.GetResource().GetName(), facts.Vendor)
		}
	}
	return nil
}
