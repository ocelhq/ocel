package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
)

const entryWorker = "entry"

func Shape(namespace string, site pricing.EdgeSite) (pricing.EdgeShape, error) {
	store, err := storeScriptNameFor(namespace, site.Tier)
	if err != nil {
		return pricing.EdgeShape{}, err
	}
	shape := pricing.EdgeShape{
		Vendor: cost.Vendor,
		Shared: []pricing.Shaped{
			{Name: workersPaidPlan, Type: cost.TypeAccountSubscription, Properties: map[string]any{"rate_plan": map[string]any{"id": workersPaidPlan}}},
			{Name: store, Type: cost.TypeWorkersScript, Properties: durableObjectScript(releasesStoreWorker)},
		},
		Environment: []pricing.Shaped{
			{Name: entryWorker, Type: cost.TypeWorkersScript, Properties: describeEntryScript()},
		},
	}
	if site.Tier == environment.TierPreview {
		shape.Shared = append(shape.Shared, pricing.Shaped{Name: previewEntryScript, Type: cost.TypeWorkersScript, Properties: describeEntryScript()})
	}
	return shape, nil
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
