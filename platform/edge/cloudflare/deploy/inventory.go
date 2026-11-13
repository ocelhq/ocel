package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
)

const (
	Vendor = "cloudflare"

	TypeAccountSubscription = "cloudflare_account_subscription"
	TypeWorkersScript       = "cloudflare_workers_script"
)

const entryWorker = "entry"

func Inventory(site pricing.EdgeSite) (pricing.EdgeInventory, error) {
	if err := refuseUnknownTier("the inventory of the cloudflare edge", site.Tier); err != nil {
		return pricing.EdgeInventory{}, err
	}
	inventory := pricing.EdgeInventory{
		Vendor: Vendor,
		Shared: []pricing.Item{
			{Name: workersPaidPlan, Type: TypeAccountSubscription, Properties: map[string]any{"rate_plan": map[string]any{"id": workersPaidPlan}}},
			{Name: releasesStoreStem, Type: TypeWorkersScript, Properties: durableObjectScript(releasesStoreWorker)},
		},
		Environment: []pricing.Item{
			{Name: entryWorker, Type: TypeWorkersScript, Properties: describeEntryScript()},
		},
	}
	if site.Tier == environment.TierPreview {
		inventory.Shared = append(inventory.Shared, pricing.Item{Name: sharedEntryStem, Type: TypeWorkersScript, Properties: describeEntryScript()})
	}
	return inventory, nil
}

func durableObjectScript(worker durableObjectWorker) map[string]any {
	bindings := make([]any, 0, len(worker.classes))
	for _, class := range worker.classes {
		bindings = append(bindings, map[string]any{"type": durableObjectBindingType, "name": class.binding, "class_name": class.className})
	}
	var created []any
	for _, step := range worker.migrations {
		for _, class := range step.sqliteClasses {
			created = append(created, class)
		}
	}
	return map[string]any{"bindings": bindings, "migrations": map[string]any{"new_sqlite_classes": created}}
}

func describeEntryScript() map[string]any {
	return map[string]any{"migrations": map[string]any{"new_sqlite_classes": []any{purgeQueueObject}}}
}
