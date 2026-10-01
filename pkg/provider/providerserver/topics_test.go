package providerserver_test

import (
	"testing"
	"time"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func runningWorkers(facts *provider.Facts) {
	facts.WorkerCeilings = []provider.WorkerCeiling{{Compute: provider.ComputeServerless, MaxDuration: 15 * time.Minute}}
	facts.Bindings = append(facts.Bindings, provider.BindingTask, provider.BindingTopic)
}

func TestAnAppIsPackedAndProvisionedWithTheTopicsTheDeployDeclares(t *testing.T) {
	builtProject(t)
	vendor := &packingProvider{Provider: fake.NewProvider(fake.Options{}).WithFacts(runningWorkers)}
	client := servedBy(t, vendor)
	bootstrappedOverRPC(t, client)

	req := deployRequest()
	req.Manifest.Resources = append(req.Manifest.Resources, topicResource(resourcesv1.ResourceType_RESOURCE_TYPE_TASK, "resize-image"))
	req.Manifest.Workers = []*contractv1.ManifestWorker{{Name: "worker", App: "web", Compute: string(provider.ComputeServerless)}}
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	declared := func(topics []provider.Resource) bool {
		return len(topics) == 1 && topics[0].Declared == "resize-image" && topics[0].Topic != nil &&
			len(topics[0].Topic.Consumers) == 1 && topics[0].Topic.Consumers[0].Worker == "worker"
	}
	specs := vendor.FakeStacks().Provisioned()
	if app := specs[len(specs)-1].App; !declared(app.Topics) {
		t.Errorf("the app spec carries topics %+v, want the task resize-image with its consumer", app.Topics)
	}
	requests := vendor.packings()
	if len(requests) != 1 || !declared(requests[0].Topics) {
		t.Errorf("the vendor was asked to pack %+v, want the declared topics handed to it", requests)
	}
}
