package cloudflare

import (
	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const entryWorker = "entry"

func Shape(namespace string, site costkit.EdgeSite) (costkit.EdgeShape, error) {
	store, err := storeScriptNameFor(namespace, site.Class)
	if err != nil {
		return costkit.EdgeShape{}, err
	}
	writer, err := isrWriterScriptNameFor(namespace, site.Class)
	if err != nil {
		return costkit.EdgeShape{}, err
	}
	cache, err := cacheStoreNameFor(namespace, site.Class)
	if err != nil {
		return costkit.EdgeShape{}, err
	}
	shape := costkit.EdgeShape{
		Vendor: cost.Vendor,
		Shared: []costkit.Shaped{
			{Name: workersPaidPlan, Type: cost.TypeAccountSubscription, Properties: map[string]any{"rate_plan": map[string]any{"id": workersPaidPlan}}},
			{Name: cache, Type: cost.TypeR2Bucket, Properties: map[string]any{"storage_class": "Standard"}},
			{Name: store, Type: cost.TypeWorkersScript, Properties: durableObjectScript(deploymentsStoreWorker)},
			{Name: writer, Type: cost.TypeWorkersScript, Properties: durableObjectScript(isrWriterWorker)},
		},
		Environment: []costkit.Shaped{
			{Name: entryWorker, Type: cost.TypeWorkersScript, Properties: map[string]any{"durable_objects": []any{}}},
		},
	}
	if site.Class == edge.ClassPreview {
		shape.Shared = append(shape.Shared, costkit.Shaped{Name: previewEntryScript, Type: cost.TypeWorkersScript, Properties: map[string]any{"durable_objects": []any{}}})
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
