package providerserver

import (
	"fmt"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RefuseUnsupportedTopicsTasksAndWorkers(facts provider.Facts, manifest *contractv1.Manifest) error {
	if len(facts.WorkerCeilings) > 0 {
		return nil
	}
	declared, found := findTopicTaskOrWorker(manifest)
	if !found {
		return nil
	}
	return refusal.Refuse(refusal.CodeUnsupported,
		"this project declares %s, and topics, tasks and workers are unsupported on %s: it names no compute a worker runs on",
		declared, facts.Vendor)
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

func workerCeilingMessages(ceilings []provider.WorkerCeiling) []*contractv1.WorkerCeiling {
	out := make([]*contractv1.WorkerCeiling, 0, len(ceilings))
	for _, ceiling := range ceilings {
		message := &contractv1.WorkerCeiling{Compute: string(ceiling.Compute)}
		if !ceiling.Unbounded {
			message.MaxDuration = durationpb.New(ceiling.MaxDuration)
		}
		out = append(out, message)
	}
	return out
}
