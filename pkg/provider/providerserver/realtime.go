package providerserver

import (
	"slices"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RefuseUnsupportedRealtime(facts provider.Facts, manifest *contractv1.Manifest) error {
	if slices.Contains(facts.Bindings, provider.BindingRealtime) {
		return nil
	}
	for _, resource := range manifest.GetResources() {
		if resource.GetResource().GetType() == resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME {
			return refusal.Refuse(refusal.CodeUnsupported,
				"this project declares realtime %q, and realtime is unsupported on %s: it runs no transport for channels",
				resource.GetResource().GetName(), facts.Vendor)
		}
	}
	return nil
}
