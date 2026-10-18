package providerserver_test

import (
	"reflect"
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

func TestAnAppIsProvisionedWithTheTopicsTheDeployDeclares(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{}).WithFacts(runningWorkers)
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
}

func TestEveryAppIsToldWhichAppHostsEachWorkerAndOnWhatCompute(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{}).WithFacts(runningWorkers)
	client := servedBy(t, vendor)
	bootstrappedOverRPC(t, client)

	req := twoAppRequest()
	req.Manifest.Resources = append(req.Manifest.Resources, topicResource(resourcesv1.ResourceType_RESOURCE_TYPE_TASK, "resize-image"))
	req.Manifest.Workers = []*contractv1.ManifestWorker{{Name: "worker", App: "admin", Compute: string(provider.ComputeServerless)}}
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	want := []provider.WorkerSpec{{Name: "worker", App: "admin", Compute: provider.ComputeServerless}}
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App == nil {
			continue
		}
		if !reflect.DeepEqual(spec.App.DeploymentWorkers, want) {
			t.Errorf("app %s was handed deployment workers %+v, want %+v: a sender must know where the worker it wakes runs", spec.App.App, spec.App.DeploymentWorkers, want)
		}
	}
}
