package conformance

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RunWorkers(t *testing.T, facts provider.Facts) {
	t.Helper()

	for _, fault := range workerFaults(facts) {
		t.Error(fault)
	}
}

func workerFaults(facts provider.Facts) []string {
	if len(facts.WorkerCeilings) == 0 {
		return unsupportedRefusalFaults(facts)
	}
	var found []string
	named := map[provider.Compute]bool{}
	for _, ceiling := range facts.WorkerCeilings {
		if named[ceiling.Compute] {
			found = append(found, fmt.Sprintf("Facts.WorkerCeilings names %q twice, and the build reads one ceiling per compute", ceiling.Compute))
		}
		named[ceiling.Compute] = true
		if !slices.Contains(facts.Computes, ceiling.Compute) {
			found = append(found, fmt.Sprintf("Facts.WorkerCeilings names %q, which is no compute in Facts.Computes, so no worker lands on it", ceiling.Compute))
		}
		if !ceiling.Unbounded && ceiling.MaxDuration <= 0 {
			found = append(found, fmt.Sprintf("the worker ceiling on %q is %v, which refuses every task and consumer on it: name a positive maximum or none", ceiling.Compute, ceiling.MaxDuration))
		}
	}
	return found
}

func unsupportedRefusalFaults(facts provider.Facts) []string {
	var found []string
	for declared, manifest := range map[string]*contractv1.Manifest{
		"a topic":  {Resources: []*contractv1.ManifestResource{declaredResource(resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, "orders")}},
		"a task":   {Resources: []*contractv1.ManifestResource{declaredResource(resourcesv1.ResourceType_RESOURCE_TYPE_TASK, "resize-image")}},
		"a worker": {Workers: []*contractv1.ManifestWorker{{Name: "media"}}},
	} {
		err := providerserver.RefuseUnsupportedTopicsTasksAndWorkers(facts, manifest)
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported {
			found = append(found, fmt.Sprintf("a deploy declaring %s on a provider naming no worker ceiling passed preflight with %v, want a refusal with code %s", declared, err, refusal.CodeUnsupported))
		}
	}
	return found
}

func declaredResource(typ resourcesv1.ResourceType, name string) *contractv1.ManifestResource {
	return &contractv1.ManifestResource{
		LogicalName: name,
		Resource:    &resourcesv1.ResourceIdentifier{Type: typ, Name: name},
		Config:      &contractv1.ManifestResource_Topic{Topic: &contractv1.ManifestTopic{}},
	}
}
