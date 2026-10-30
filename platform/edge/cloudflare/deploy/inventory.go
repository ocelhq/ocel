package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
)

const entryWorker = "entry"

func Inventory(site pricing.EdgeSite) (pricing.EdgeInventory, error) {
	if err := refuseUnknownTier("the inventory of the cloudflare edge", site.Tier); err != nil {
		return pricing.EdgeInventory{}, err
	}
	inventory := pricing.EdgeInventory{
		Vendor: cost.Vendor,
		Shared: []pricing.Resource{
			{Name: workersPaidPlan, Type: cost.TypeAccountSubscription, Properties: map[string]any{"rate_plan": map[string]any{"id": workersPaidPlan}}},
			{Name: releasesStoreStem, Type: cost.TypeWorkersScript, Properties: durableObjectScript(releasesStoreWorker)},
		},
		Environment: []pricing.Resource{
			{Name: entryWorker, Type: cost.TypeWorkersScript, Properties: describeEntryScript()},
		},
	}
	if site.Tier == environment.TierPreview {
		inventory.Shared = append(inventory.Shared, pricing.Resource{Name: sharedEntryStem, Type: cost.TypeWorkersScript, Properties: describeEntryScript()})
	}
	return inventory, nil
}

func durableObjectScript(worker durableObjectWorker) map[string]any {
	classes := make([]any, 0, len(worker.classes))
	for _, class := range worker.classes {
		classes = append(classes, class.className)
	}
	return map[string]any{"durable_objects": classes}
}

func describeEntryScript() map[string]any {
	return map[string]any{"durable_objects": []any{purgeQueueObject}}
}
