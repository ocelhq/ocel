package providerserver

import (
	"fmt"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RefuseUnsupportedTopicsTasksAndWorkers(facts provider.Facts, manifest *contractv1.Manifest) error {
	if len(facts.WorkerCeilings) > 0 {
		return refuseWorkersOnUnservedComputes(facts, manifest)
	}
	declared, found := findTopicTaskOrWorker(manifest)
	if !found {
		return nil
	}
	return refusal.Refuse(refusal.CodeUnsupported,
		"this project declares %s, and topics, tasks and workers are unsupported on %s: it names no compute a worker runs on",
		declared, facts.Vendor)
}

func refuseInvalidWorkers(manifest *contractv1.Manifest) error {
	seen := map[string]bool{}
	for _, worker := range manifest.GetWorkers() {
		name := worker.GetName()
		if err := naming.Validate("worker name", name); err != nil {
			return refusal.Refuse(refusal.CodeInvalid, "%s", err.Error())
		}
		if seen[name] {
			return refusal.Refuse(refusal.CodeInvalid, "this manifest declares worker %q twice, and tasks and consumers name the one worker they run on", name)
		}
		seen[name] = true
		if err := provider.RefuseConcurrency(worker.GetConcurrency()); err != nil {
			return refusal.Refuse(refusal.CodeInvalid, "worker %q %s", name, err)
		}
	}
	return nil
}

func readWorkersByApp(manifest *contractv1.Manifest) (map[string][]provider.WorkerSpec, error) {
	if err := refuseInvalidWorkers(manifest); err != nil {
		return nil, err
	}
	computes := make(map[string]provider.Compute, len(manifest.GetApps()))
	for _, app := range manifest.GetApps() {
		computes[app.GetName()] = provider.ComputeOf(app)
	}
	byApp := map[string][]provider.WorkerSpec{}
	for _, worker := range manifest.GetWorkers() {
		name := worker.GetName()
		compute, found := computes[worker.GetApp()]
		if !found {
			return nil, refusal.Refuse(refusal.CodeInvalid, "worker %q joins app %q, and this manifest deploys no app by that name", name, worker.GetApp())
		}
		if provider.Compute(worker.GetCompute()) != compute {
			return nil, refusal.Refuse(refusal.CodeInvalid, "worker %q runs on %q compute, and its app %q runs on %q: a worker runs on its app's compute",
				name, worker.GetCompute(), worker.GetApp(), compute)
		}
		byApp[worker.GetApp()] = append(byApp[worker.GetApp()], provider.WorkerSpec{Name: name, Concurrency: int(worker.GetConcurrency())})
	}
	return byApp, nil
}

func refuseConsumerOnUndeclaredWorker(manifest *contractv1.Manifest, resource provider.Resource) error {
	if resource.Topic == nil {
		return nil
	}
	for _, consumer := range resource.Topic.Consumers {
		declared := slices.ContainsFunc(manifest.GetWorkers(), func(worker *contractv1.ManifestWorker) bool {
			return worker.GetName() == consumer.Worker
		})
		if !declared {
			return refusal.Refuse(refusal.CodeInvalid, "%s %s: consumer %q runs on worker %q, and this manifest declares no worker by that name",
				resource.Type, resource.Declared, consumer.Name, consumer.Worker)
		}
	}
	return nil
}

func findTopicTaskOrWorker(manifest *contractv1.Manifest) (string, bool) {
	for _, resource := range manifest.GetResources() {
		switch typ := resource.GetResource().GetType(); typ {
		case resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, resourcesv1.ResourceType_RESOURCE_TYPE_TASK:
			return fmt.Sprintf("%s %q", naming.ResourceTypeName(typ), resource.GetResource().GetName()), true
		}
	}
	if workers := manifest.GetWorkers(); len(workers) > 0 {
		return fmt.Sprintf("worker %q", workers[0].GetName()), true
	}
	return "", false
}

func refuseWorkersOnUnservedComputes(facts provider.Facts, manifest *contractv1.Manifest) error {
	for _, worker := range manifest.GetWorkers() {
		compute := provider.Compute(worker.GetCompute())
		if !slices.ContainsFunc(facts.WorkerCeilings, func(ceiling provider.WorkerCeiling) bool { return ceiling.Compute == compute }) {
			return refusal.Refuse(refusal.CodeUnsupported,
				"worker %q runs on app %q's %s compute, and %s runs no worker on %s compute",
				worker.GetName(), worker.GetApp(), compute, facts.Vendor, compute)
		}
	}
	return nil
}

func (r *deployRun) declaredTopics() ([]provider.Resource, error) {
	resources, err := manifestResources(r.manifest)
	if err != nil {
		return nil, err
	}
	var topics []provider.Resource
	for _, resource := range resources {
		if resource.Topic != nil {
			topics = append(topics, resource)
		}
	}
	return topics, nil
}
