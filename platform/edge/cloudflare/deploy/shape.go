package cloudflare

import (
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	Vendor = "cloudflare"

	TypeAccountSubscription = "cloudflare_account_subscription"
	TypeWorkersScript       = "cloudflare_workers_script"
	TypeR2Bucket            = "cloudflare_r2_bucket"

	entryWorker = "entry"
)

type Shaped struct {
	Name       string
	Type       string
	Properties map[string]any
}

type Shape struct {
	Shared      []Shaped
	Environment []Shaped
}

func ShapeEdge(namespace string, class edge.Class) (Shape, error) {
	store, err := storeScriptNameFor(namespace, class)
	if err != nil {
		return Shape{}, err
	}
	writer, err := isrWriterScriptNameFor(namespace, class)
	if err != nil {
		return Shape{}, err
	}
	cache, err := cacheStoreNameFor(namespace, class)
	if err != nil {
		return Shape{}, err
	}
	shape := Shape{
		Shared: []Shaped{
			{Name: workersPaidPlan, Type: TypeAccountSubscription, Properties: map[string]any{"rate_plan": map[string]any{"id": workersPaidPlan}}},
			{Name: cache, Type: TypeR2Bucket, Properties: map[string]any{"storage_class": "Standard"}},
			{Name: store, Type: TypeWorkersScript, Properties: durableObjectScript(deploymentsStoreWorker)},
			{Name: writer, Type: TypeWorkersScript, Properties: durableObjectScript(isrWriterWorker)},
		},
		Environment: []Shaped{
			{Name: entryWorker, Type: TypeWorkersScript, Properties: map[string]any{"durable_objects": []any{}}},
		},
	}
	if class == edge.ClassPreview {
		shape.Shared = append(shape.Shared, Shaped{Name: previewEntryScript, Type: TypeWorkersScript, Properties: map[string]any{"durable_objects": []any{}}})
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
