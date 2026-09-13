package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const entryWorker = "entry"

var (
	_ costkit.EdgeInventorier = (*provider)(nil)
	_ costkit.EdgePricer      = (*provider)(nil)
)

func (p *provider) CostInventory(site costkit.EdgeSite) (costkit.EdgeInventory, error) {
	store, err := storeScriptNameFor(p.namespace, site.Class)
	if err != nil {
		return costkit.EdgeInventory{}, err
	}
	writer, err := isrWriterScriptNameFor(p.namespace, site.Class)
	if err != nil {
		return costkit.EdgeInventory{}, err
	}
	cache, err := cacheStoreNameFor(p.namespace, site.Class)
	if err != nil {
		return costkit.EdgeInventory{}, err
	}
	inventory := costkit.EdgeInventory{
		Vendor: cost.Vendor,
		Shared: []costkit.Item{
			{Name: workersPaidPlan, Type: cost.TypeAccountSubscription, Properties: map[string]any{"rate_plan": map[string]any{"id": workersPaidPlan}}},
			{Name: cache, Type: cost.TypeR2Bucket, Properties: map[string]any{"storage_class": "Standard"}},
			{Name: store, Type: cost.TypeWorkersScript, Properties: durableObjectScript(deploymentsStoreWorker)},
			{Name: writer, Type: cost.TypeWorkersScript, Properties: durableObjectScript(isrWriterWorker)},
		},
		Environment: []costkit.Item{
			{Name: entryWorker, Type: cost.TypeWorkersScript, Properties: map[string]any{"durable_objects": []any{}}},
		},
	}
	if site.Class == edge.ClassPreview {
		inventory.Shared = append(inventory.Shared, costkit.Item{Name: previewEntryScript, Type: cost.TypeWorkersScript, Properties: map[string]any{"durable_objects": []any{}}})
	}
	return inventory, nil
}

func (p *provider) CostCard() (*costkit.Card, error) { return cost.Card() }

func (p *provider) CostTable() costkit.Table { return cost.Table }

func durableObjectScript(worker durableObjectWorker) map[string]any {
	classes := make([]any, 0, len(worker.classes))
	for _, class := range worker.classes {
		classes = append(classes, class.className)
	}
	return map[string]any{"durable_objects": classes}
}
