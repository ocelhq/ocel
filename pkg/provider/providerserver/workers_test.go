package providerserver_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func topicResource(typ resourcesv1.ResourceType, name string) *contractv1.ManifestResource {
	return &contractv1.ManifestResource{
		LogicalName: "topic--" + name,
		Resource:    &resourcesv1.ResourceIdentifier{Type: typ, Name: name},
		Config: &contractv1.ManifestResource_Topic{Topic: &contractv1.ManifestTopic{
			Consumers: []*contractv1.ManifestConsumer{{Name: name, Worker: "worker", Exclusive: typ == resourcesv1.ResourceType_RESOURCE_TYPE_TASK}},
		}},
	}
}

func topicTaskAndWorkerManifests() map[string]*contractv1.Manifest {
	return map[string]*contractv1.Manifest{
		"a topic":  {Slug: "shop", Resources: []*contractv1.ManifestResource{topicResource(resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, "orders")}},
		"a task":   {Slug: "shop", Resources: []*contractv1.ManifestResource{topicResource(resourcesv1.ResourceType_RESOURCE_TYPE_TASK, "resize-image")}},
		"a worker": {Slug: "shop", Workers: []*contractv1.ManifestWorker{{Name: "media", App: "media", Compute: string(provider.ComputeServerless)}}},
	}
}

func TestAProviderNamingNoWorkerCeilingRefusesTopicsTasksAndWorkersAsUnsupported(t *testing.T) {
	t.Parallel()

	facts := fake.NewProvider(fake.Options{}).Facts()
	for declared, manifest := range topicTaskAndWorkerManifests() {
		t.Run(declared, func(t *testing.T) {
			t.Parallel()

			err := providerserver.RefuseUnsupportedTopicsTasksAndWorkers(facts, manifest)
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported {
				t.Fatalf("RefuseUnsupportedTopicsTasksAndWorkers() = %v, want a refusal with code %s", err, refusal.CodeUnsupported)
			}
			if !strings.Contains(err.Error(), "unsupported") || !strings.Contains(err.Error(), string(facts.Vendor)) {
				t.Errorf("RefuseUnsupportedTopicsTasksAndWorkers() = %q, want it to say it is unsupported on %s", err, facts.Vendor)
			}
		})
	}
}

func TestAProviderNamingAWorkerCeilingIsNotRefusedTopicsTasksOrWorkers(t *testing.T) {
	t.Parallel()

	facts := fake.NewProvider(fake.Options{}).Facts()
	facts.WorkerCeilings = []provider.WorkerCeiling{{Compute: provider.ComputeServerless, MaxDuration: 15 * time.Minute}}
	for declared, manifest := range topicTaskAndWorkerManifests() {
		if err := providerserver.RefuseUnsupportedTopicsTasksAndWorkers(facts, manifest); err != nil {
			t.Errorf("RefuseUnsupportedTopicsTasksAndWorkers(%s) = %v, want nil from a provider that runs workers", declared, err)
		}
	}
}

func TestAWorkerOnAComputeTheProviderNamesNoCeilingForIsRefusedAsUnsupported(t *testing.T) {
	t.Parallel()

	facts := fake.NewProvider(fake.Options{}).Facts()
	facts.WorkerCeilings = []provider.WorkerCeiling{{Compute: provider.ComputeServerless, MaxDuration: 15 * time.Minute}}
	manifest := &contractv1.Manifest{Slug: "shop", Workers: []*contractv1.ManifestWorker{
		{Name: "worker", App: "web", Compute: string(provider.ComputeServerless)},
		{Name: "media", App: "media", Compute: string(provider.ComputeContainer)},
	}}

	err := providerserver.RefuseUnsupportedTopicsTasksAndWorkers(facts, manifest)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeUnsupported {
		t.Fatalf("RefuseUnsupportedTopicsTasksAndWorkers() = %v, want a refusal with code %s", err, refusal.CodeUnsupported)
	}
	for _, named := range []string{`"media"`, string(provider.ComputeContainer), string(facts.Vendor)} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("RefuseUnsupportedTopicsTasksAndWorkers() = %q, want it to name %s", err, named)
		}
	}
}

func TestAManifestDeclaringNoTopicTaskOrWorkerIsNeverRefusedForThem(t *testing.T) {
	t.Parallel()

	facts := fake.NewProvider(fake.Options{}).Facts()
	if err := providerserver.RefuseUnsupportedTopicsTasksAndWorkers(facts, deployRequest().GetManifest()); err != nil {
		t.Errorf("RefuseUnsupportedTopicsTasksAndWorkers() = %v, want nil for a manifest with no topic, task or worker", err)
	}
}

func TestADeployDeclaringATaskIsRefusedAtPreflightBeforeAnythingUploads(t *testing.T) {
	builtProject(t)
	vendor := &preflighting{Provider: fake.NewProvider(fake.Options{})}
	client := servedBy(t, vendor)

	req := deployRequest()
	req.Manifest.Resources = append(req.Manifest.Resources, topicResource(resourcesv1.ResourceType_RESOURCE_TYPE_TASK, "resize-image"))
	req.Manifest.Workers = []*contractv1.ManifestWorker{{Name: "worker", App: "web", Compute: string(provider.ComputeServerless)}}
	stream, err := client.Deploy(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var failure string
	for stream.Receive() {
		if result := stream.Msg().GetResult(); result != nil {
			failure = result.GetError()
		}
	}
	said := failure + connectMessage(stream.Err())
	stream.Close()

	if !strings.Contains(said, "unsupported") {
		t.Fatalf("Deploy() said %q, want the task refused as unsupported", said)
	}
	if uploaded := vendor.uploads(); len(uploaded) != 0 {
		t.Errorf("the deploy uploaded %v before refusing, want nothing put in the store", uploaded)
	}
	if ran := vendor.Preflighted(); len(ran) != 0 {
		t.Errorf("the vendor's own preflight ran %d times, want the refusal to come first", len(ran))
	}
}

func TestADeployHandsEachAppStackTheWorkersThatJoinIt(t *testing.T) {
	builtProject(t)
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t)).WithFacts(func(facts *provider.Facts) {
		facts.WorkerCeilings = []provider.WorkerCeiling{{Compute: provider.ComputeServerless, MaxDuration: 15 * time.Minute}}
	})
	client := servedBy(t, vendor)

	req := twoAppRequest()
	req.Manifest.Workers = []*contractv1.ManifestWorker{{Name: "media", Concurrency: 4, App: "admin", Compute: string(provider.ComputeServerless)}}
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	workers := map[string][]provider.WorkerSpec{}
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App != nil {
			workers[spec.App.App] = spec.App.Workers
		}
	}
	want := map[string][]provider.WorkerSpec{"web": nil, "admin": {{Name: "media", Concurrency: 4}}}
	if !reflect.DeepEqual(workers, want) {
		t.Errorf("app stacks were handed workers %+v, want %+v", workers, want)
	}
}

func TestConfigureNamesTheLongestAWorkerRunsOnEachCompute(t *testing.T) {
	t.Parallel()

	vendor := fake.NewProvider(fake.Options{Region: "nowhere"}).WithFacts(func(facts *provider.Facts) {
		facts.WorkerCeilings = []provider.WorkerCeiling{
			{Compute: provider.ComputeServerless, MaxDuration: 15 * time.Minute},
			{Compute: provider.ComputeContainer, Unbounded: true},
		}
	})
	_, facts := configuredProvider(t, "1.0.0", vendor)
	got := map[string]*durationpb.Duration{}
	for _, ceiling := range facts.GetWorkerCeilings() {
		got[ceiling.GetCompute()] = ceiling.GetMaxDuration()
	}
	if len(got) != 2 {
		t.Fatalf("Configure() worker ceilings = %v, want one per compute the provider runs workers on", facts.GetWorkerCeilings())
	}
	if serverless := got[string(provider.ComputeServerless)]; serverless.AsDuration() != 15*time.Minute {
		t.Errorf("serverless ceiling = %v, want 15m", serverless)
	}
	if container, named := got[string(provider.ComputeContainer)]; !named || container != nil {
		t.Errorf("container ceiling = %v (named %v), want it named with no maximum", container, named)
	}
}

func TestConfigureNamesNoWorkerCeilingForAProviderThatRunsNoWorkers(t *testing.T) {
	t.Parallel()

	_, facts := configuredProvider(t, "1.0.0", fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t)))
	if ceilings := facts.GetWorkerCeilings(); len(ceilings) != 0 {
		t.Errorf("Configure() worker ceilings = %v, want none from a provider that runs no workers", ceilings)
	}
}
