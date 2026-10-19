package providerserver_test

import (
	"errors"
	"maps"
	"slices"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func containerApp(name string) *contractv1.ManifestApp {
	return &contractv1.ManifestApp{
		Name:     name,
		Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{Image: "registry.test/" + name + "@sha256:abc"}},
	}
}

func taskResource(name string, consumers ...*contractv1.ManifestConsumer) *contractv1.ManifestResource {
	return &contractv1.ManifestResource{
		LogicalName: "task--" + name,
		Resource:    &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: name},
		Config:      &contractv1.ManifestResource_Topic{Topic: &contractv1.ManifestTopic{Consumers: consumers}},
	}
}

func isInvalid(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeInvalid
}

func TestReadResourcesNamesEachResourceByItsLogicalNameAndKeepsTheDeclaredOne(t *testing.T) {
	t.Parallel()

	manifest := &contractv1.Manifest{
		Slug:      "shop",
		Workers:   []*contractv1.ManifestWorker{{Name: "worker", App: "web", Compute: "container"}},
		Resources: []*contractv1.ManifestResource{taskResource("resize", &contractv1.ManifestConsumer{Name: "resize", Worker: "worker", Exclusive: true})},
	}
	resources, err := providerserver.ReadResources(manifest)
	if err != nil {
		t.Fatalf("ReadResources: %v", err)
	}
	if len(resources) != 1 || resources[0].Name != "task--resize" || resources[0].Declared != "resize" || resources[0].Type != provider.BindingTask {
		t.Fatalf("ReadResources = %+v, want task--resize declared as resize", resources)
	}
}

func TestReadResourcesRefusesATaskWithoutItsOneExclusiveConsumer(t *testing.T) {
	t.Parallel()

	manifest := &contractv1.Manifest{
		Slug:      "shop",
		Workers:   []*contractv1.ManifestWorker{{Name: "worker", App: "web", Compute: "container"}},
		Resources: []*contractv1.ManifestResource{taskResource("resize", &contractv1.ManifestConsumer{Name: "resize", Worker: "worker"})},
	}
	if _, err := providerserver.ReadResources(manifest); !isInvalid(err) {
		t.Fatalf("ReadResources = %v, want a refusal as invalid", err)
	}
}

func TestReadWorkersRefusesAWorkerOnAnotherComputeThanItsApp(t *testing.T) {
	t.Parallel()

	manifest := &contractv1.Manifest{
		Slug:    "shop",
		Apps:    []*contractv1.ManifestApp{containerApp("web")},
		Workers: []*contractv1.ManifestWorker{{Name: "worker", App: "web", Compute: "serverless"}},
	}
	if _, err := providerserver.ReadWorkers(manifest); !isInvalid(err) {
		t.Fatalf("ReadWorkers = %v, want a refusal as invalid", err)
	}

	manifest.Workers[0].Compute = "container"
	workers, err := providerserver.ReadWorkers(manifest)
	if err != nil {
		t.Fatalf("ReadWorkers: %v", err)
	}
	if len(workers) != 1 || workers[0].Name != "worker" || workers[0].App != "web" {
		t.Errorf("ReadWorkers = %+v, want web's one worker", workers)
	}
}

func TestReadBoundNamesUsedByNamesWhatAnAppsUsagesReachByItsBinding(t *testing.T) {
	t.Parallel()

	manifest := &contractv1.Manifest{
		Usages: []*contractv1.ManifestUsage{
			{App: "web", Resource: "postgres--main"},
			{App: "web", Resource: "bucket--uploads"},
			{App: "docs", Resource: "kv--cache"},
		},
	}
	resources := []provider.Resource{
		{Name: "postgres--main", Declared: "main", Type: provider.BindingPostgres},
		{Name: "bucket--uploads", Declared: "uploads", Type: provider.BindingBucket, Binding: "shared-uploads"},
		{Name: "kv--cache", Declared: "cache", Type: provider.BindingKV},
	}
	got := providerserver.ReadBoundNamesUsedBy(manifest, resources, "web")
	if names := slices.Sorted(maps.Keys(got)); !slices.Equal(names, []string{"postgres--main", "shared-uploads"}) {
		t.Errorf("ReadBoundNamesUsedBy(web) = %v, want postgres--main and the binding shared-uploads", names)
	}
}
