package manifest

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestPlaceConsumersPutsATaskWithNoWorkerOnTheDefaultWorkerInTheProjectsOneApp(t *testing.T) {
	dir := t.TempDir()
	cfg := &project.Project{Dir: dir, Apps: []project.App{{Name: "web", Path: "."}}}

	source := filepath.Join(dir, "jobs", "index.ts") + ":3"
	placement, err := PlaceConsumers(cfg, []declaration.Resource{{
		Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "greet", Task: &resourcesv1.TaskConfig{},
		Source: source,
	}})
	if err != nil {
		t.Fatalf("PlaceConsumers: %v", err)
	}
	consumers := placement.Topics["greet"].GetConsumers()
	if len(consumers) != 1 || consumers[0].GetName() != "greet" || consumers[0].GetWorker() != "worker" || !consumers[0].GetExclusive() {
		t.Errorf("topics = %v, want task greet as a topic whose one exclusive consumer runs on worker", placement.Topics)
	}
	if workers := placement.Workers; len(workers) != 1 || workers[0].GetName() != "worker" || workers[0].GetApp() != "web" {
		t.Errorf("workers = %v, want the default worker joined to the one app", workers)
	}
	if sources := placement.Sources["worker"]; len(sources) != 1 || sources[0] != source {
		t.Errorf("sources = %v, want worker serving what is declared at %s", placement.Sources, source)
	}
}

func TestPlaceConsumersRecordsNoSourcesForAWorkerThatServesNothing(t *testing.T) {
	dir := t.TempDir()
	cfg := &project.Project{Dir: dir, Apps: []project.App{{Name: "web", Path: "."}}}

	placement, err := PlaceConsumers(cfg, []declaration.Resource{{
		Type: resourcesv1.ResourceType_RESOURCE_TYPE_WORKER, Name: "media", Worker: &resourcesv1.WorkerConfig{},
		Source: filepath.Join(dir, "jobs", "index.ts") + ":3",
	}})
	if err != nil {
		t.Fatalf("PlaceConsumers: %v", err)
	}
	if len(placement.Workers) != 1 || len(placement.Sources["media"]) != 0 {
		t.Errorf("placement = %+v, want worker media placed and serving nothing", placement)
	}
}

func TestPlaceConsumersRefusesWhatTheBuildRefusesNamingTheDeclaration(t *testing.T) {
	dir := t.TempDir()
	cfg := &project.Project{Dir: dir, Apps: []project.App{{Name: "web", Path: "."}}}

	_, err := PlaceConsumers(cfg, []declaration.Resource{{
		Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "greet", Task: &resourcesv1.TaskConfig{Worker: "media"},
		Source: filepath.Join(dir, "jobs", "index.ts") + ":3",
	}})
	var invalid *InvalidDeclarationError
	if !errors.As(err, &invalid) || invalid.Source != "jobs/index.ts:3" {
		t.Fatalf("err = %v, want the refusal of a task on an undeclared worker, naming jobs/index.ts:3", err)
	}
}
