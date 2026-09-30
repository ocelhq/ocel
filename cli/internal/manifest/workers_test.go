package manifest

import (
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const pinnedImage = "registry.example/jobs@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func assembleWorkers(apps []app, declarations []declaredResource, ceilings []provider.WorkerCeiling) (*contractv1.Manifest, error) {
	var functions []build.Function
	for i := range apps {
		if apps[i].Compute == "" {
			apps[i].Compute = provider.ComputeServerless
		}
		if apps[i].Compute == provider.ComputeContainer {
			apps[i].Image = pinnedImage
			continue
		}
		functions = append(functions, build.Function{
			Route: "index", App: apps[i].Name, Framework: buildoutput.Framework{Name: "node"},
			EntryFile: "index.js", ArtifactPath: "dist/" + apps[i].Name + ".zip",
		})
	}
	return assemble(assembly{slug: "shop", apps: apps, declarations: declarations, functions: functions, ceilings: ceilings})
}

func task(name, source string, config *resourcesv1.TaskConfig) declaredResource {
	if config == nil {
		config = &resourcesv1.TaskConfig{}
	}
	return declaredResource{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: name, Task: config, Source: source}
}

func topic(name, source string, config *resourcesv1.TopicConfig) declaredResource {
	if config == nil {
		config = &resourcesv1.TopicConfig{}
	}
	return declaredResource{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, Name: name, Topic: config, Source: source}
}

func consumer(name, source string, config *resourcesv1.ConsumerConfig) declaredResource {
	return declaredResource{Type: resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER, Name: name, Consumer: config, Source: source}
}

func worker(name, source string, config *resourcesv1.WorkerConfig) declaredResource {
	if config == nil {
		config = &resourcesv1.WorkerConfig{}
	}
	return declaredResource{Type: resourcesv1.ResourceType_RESOURCE_TYPE_WORKER, Name: name, Worker: config, Source: source}
}

func workerNames(m *contractv1.Manifest) []string {
	var names []string
	for _, w := range m.GetWorkers() {
		names = append(names, w.GetName())
	}
	return names
}

func findTopic(m *contractv1.Manifest, logical string) *contractv1.ManifestTopic {
	for _, r := range m.GetResources() {
		if r.GetLogicalName() == logical {
			return r.GetTopic()
		}
	}
	return nil
}

func refusedAt(t *testing.T, err error, source string, words ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("assemble() = nil, want a refusal naming %s", source)
	}
	for _, want := range append([]string{source}, words...) {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("assemble() error = %q, want it to name %q", err, want)
		}
	}
}

func TestATaskWithNoWorkerLandsOnTheDefaultWorkerOfTheOnlyApp(t *testing.T) {
	t.Parallel()

	m, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{task("resize", "src/resize.ts:3", nil)}, nil)
	if err != nil {
		t.Fatalf("assemble() = %v", err)
	}
	if got := workerNames(m); len(got) != 1 || got[0] != "worker" {
		t.Errorf("workers = %v, want the default worker named \"worker\"", got)
	}
	consumers := findTopic(m, "topic--resize").GetConsumers()
	if len(consumers) != 1 || consumers[0].GetWorker() != "worker" {
		t.Errorf("the task's consumer = %v, want it served by the default worker", consumers)
	}
}

func TestADefaultWorkerAmongSeveralAppsAsksForAnEntry(t *testing.T) {
	t.Parallel()

	_, err := assembleWorkers([]app{{Name: "web"}, {Name: "admin"}}, []declaredResource{task("resize", "src/resize.ts:3", nil)}, nil)
	refusedAt(t, err, "src/resize.ts:3", `"worker"`, "ocel.json")
}

func TestAnAppNamedWorkerServesTheDefaultAmongSeveralApps(t *testing.T) {
	t.Parallel()

	m, err := assembleWorkers([]app{{Name: "web"}, {Name: "worker", Compute: provider.ComputeContainer}}, []declaredResource{task("resize", "src/resize.ts:3", nil)}, nil)
	if err != nil {
		t.Fatalf("assemble() = %v", err)
	}
	if got := workerNames(m); len(got) != 1 || got[0] != "worker" {
		t.Errorf("workers = %v, want the app named worker", got)
	}
}

func TestAWorkerJoinsTheAppItIsNamedFor(t *testing.T) {
	t.Parallel()

	m, err := assembleWorkers(
		[]app{{Name: "web"}, {Name: "media", Compute: provider.ComputeContainer}},
		[]declaredResource{
			worker("media", "src/media.ts:1", &resourcesv1.WorkerConfig{Concurrency: 4}),
			task("resize", "src/resize.ts:3", &resourcesv1.TaskConfig{Worker: "media"}),
		}, nil)
	if err != nil {
		t.Fatalf("assemble() = %v", err)
	}
	if got := m.GetWorkers(); len(got) != 1 || got[0].GetName() != "media" || got[0].GetConcurrency() != 4 {
		t.Errorf("workers = %v, want media alone with its concurrency", got)
	}
}

func TestAWorkerNamedLikeAnAppThatTakesPublicTrafficIsRefused(t *testing.T) {
	t.Parallel()

	for reason, media := range map[string]app{
		"domains":     {Name: "media", Domains: []string{"media.example.com"}},
		"health path": {Name: "media", Compute: provider.ComputeContainer, HealthCheckPath: "/healthz"},
	} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()

			_, err := assembleWorkers([]app{{Name: "web"}, media}, []declaredResource{worker("media", "src/media.ts:1", nil)}, nil)
			refusedAt(t, err, "src/media.ts:1", reason)
		})
	}
}

func TestATaskOrConsumerNamingAWorkerNothingDeclaresIsServedByNone(t *testing.T) {
	t.Parallel()

	for name, declarations := range map[string][]declaredResource{
		"task": {task("resize", "src/resize.ts:3", &resourcesv1.TaskConfig{Worker: "media"})},
		"consumer": {
			topic("orders", "src/orders.ts:1", nil),
			consumer("email", "src/email.ts:7", &resourcesv1.ConsumerConfig{Topic: "orders", Worker: "media"}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := assembleWorkers([]app{{Name: "web"}}, declarations, nil)
			source := declarations[len(declarations)-1].Source
			refusedAt(t, err, source, `"media"`, "no worker")
		})
	}
}

func TestATaskOrConsumerDeclaredOnTwoWorkersIsRefusedNamingBoth(t *testing.T) {
	t.Parallel()

	for name, declarations := range map[string][]declaredResource{
		"task": {
			worker("media", "src/media.ts:1", nil),
			task("resize", "src/a.ts:3", &resourcesv1.TaskConfig{Worker: "media"}),
			task("resize", "src/b.ts:9", nil),
		},
		"consumer": {
			worker("media", "src/media.ts:1", nil),
			topic("orders", "src/orders.ts:1", nil),
			consumer("email", "src/a.ts:3", &resourcesv1.ConsumerConfig{Topic: "orders", Worker: "media"}),
			consumer("email", "src/b.ts:9", &resourcesv1.ConsumerConfig{Topic: "orders"}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := assembleWorkers([]app{{Name: "web"}}, declarations, nil)
			refusedAt(t, err, "src/a.ts:3", "src/b.ts:9", `"media"`, `"worker"`)
		})
	}
}

func TestAMaxDurationAboveTheCeilingOfTheWorkersComputeIsRefused(t *testing.T) {
	t.Parallel()

	ceilings := []provider.WorkerCeiling{{Compute: provider.ComputeServerless, MaxDuration: 15 * time.Minute}}
	for name, declarations := range map[string][]declaredResource{
		"task": {task("resize", "src/resize.ts:3", &resourcesv1.TaskConfig{MaxDuration: durationpb.New(20 * time.Minute)})},
		"consumer": {
			topic("orders", "src/orders.ts:1", nil),
			consumer("email", "src/email.ts:7", &resourcesv1.ConsumerConfig{Topic: "orders", MaxDuration: durationpb.New(20 * time.Minute)}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := assembleWorkers([]app{{Name: "web"}}, declarations, ceilings)
			refusedAt(t, err, declarations[len(declarations)-1].Source, "20m", "15m", "serverless")
		})
	}
}

func TestAMaxDurationWithinTheCeilingOrOnAnUnboundedComputeIsAccepted(t *testing.T) {
	t.Parallel()

	ceilings := []provider.WorkerCeiling{
		{Compute: provider.ComputeServerless, MaxDuration: 15 * time.Minute},
		{Compute: provider.ComputeContainer, Unbounded: true},
	}
	declarations := []declaredResource{
		worker("media", "src/media.ts:1", nil),
		task("resize", "src/resize.ts:3", &resourcesv1.TaskConfig{MaxDuration: durationpb.New(15 * time.Minute)}),
		task("transcode", "src/transcode.ts:3", &resourcesv1.TaskConfig{Worker: "media", MaxDuration: durationpb.New(12 * time.Hour)}),
	}
	apps := []app{{Name: "web"}, {Name: "media", Compute: provider.ComputeContainer}, {Name: "worker"}}
	if _, err := assembleWorkers(apps, declarations, ceilings); err != nil {
		t.Errorf("assemble() = %v, want both tasks within their worker's ceiling", err)
	}
}

func TestANamedWorkerWithNoAppTakesTheComputeOfTheAppDeclaringIt(t *testing.T) {
	t.Parallel()

	ceilings := []provider.WorkerCeiling{
		{Compute: provider.ComputeServerless, MaxDuration: 15 * time.Minute},
		{Compute: provider.ComputeContainer, Unbounded: true},
	}
	apps := func() []app {
		return []app{{Name: "web", Path: "apps/web"}, {Name: "jobs", Path: "apps/jobs", Compute: provider.ComputeContainer}}
	}
	long := &resourcesv1.TaskConfig{Worker: "media", MaxDuration: durationpb.New(time.Hour)}

	if _, err := assembleWorkers(apps(), []declaredResource{
		worker("media", "apps/jobs/src/media.ts:1", nil),
		task("transcode", "apps/jobs/src/transcode.ts:3", long),
	}, ceilings); err != nil {
		t.Errorf("assemble() = %v, want a worker declared in the container app to run on container compute", err)
	}

	_, err := assembleWorkers(apps(), []declaredResource{
		worker("media", "apps/web/src/media.ts:1", nil),
		task("transcode", "apps/web/src/transcode.ts:3", long),
	}, ceilings)
	refusedAt(t, err, "apps/web/src/transcode.ts:3", "15m")
}

func TestEachWorkerNamesTheAppWhoseFolderAndComputeItRunsOn(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		apps         []app
		declarations []declaredResource
		want         *contractv1.ManifestWorker
	}{
		"the default worker of the only app": {
			apps:         []app{{Name: "web", Path: "."}},
			declarations: []declaredResource{task("resize", "src/resize.ts:3", nil)},
			want:         &contractv1.ManifestWorker{Name: "worker", App: "web", Path: ".", Compute: "serverless"},
		},
		"a worker joining the app it is named for": {
			apps: []app{{Name: "web", Path: "apps/web"}, {Name: "media", Path: "apps/media", Compute: provider.ComputeContainer}},
			declarations: []declaredResource{
				worker("media", "apps/web/src/media.ts:1", &resourcesv1.WorkerConfig{Concurrency: 4}),
				task("resize", "apps/web/src/resize.ts:3", &resourcesv1.TaskConfig{Worker: "media"}),
			},
			want: &contractv1.ManifestWorker{Name: "media", Concurrency: 4, App: "media", Path: "apps/media", Compute: "container"},
		},
		"a named worker with no app of its name": {
			apps: []app{{Name: "web", Path: "apps/web"}, {Name: "jobs", Path: "apps/jobs", Compute: provider.ComputeContainer}},
			declarations: []declaredResource{
				worker("media", "apps/jobs/src/media.ts:1", nil),
				task("resize", "apps/jobs/src/resize.ts:3", &resourcesv1.TaskConfig{Worker: "media"}),
			},
			want: &contractv1.ManifestWorker{Name: "media", App: "jobs", Path: "apps/jobs", Compute: "container"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m, err := assembleWorkers(tc.apps, tc.declarations, nil)
			if err != nil {
				t.Fatalf("assemble() = %v", err)
			}
			if got := m.GetWorkers(); len(got) != 1 || !proto.Equal(got[0], tc.want) {
				t.Errorf("workers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAWorkerDeclaredTwiceIsADuplicate(t *testing.T) {
	t.Parallel()

	_, err := assembleWorkers([]app{{Name: "web"}}, []declaredResource{
		worker("media", "src/a.ts:1", nil),
		worker("media", "src/b.ts:1", nil),
	}, nil)
	var duplicate *DuplicateError
	if !errors.As(err, &duplicate) || duplicate.FirstSource != "src/a.ts:1" || duplicate.SecondSource != "src/b.ts:1" {
		t.Errorf("assemble() = %v, want a DuplicateError naming both declarations", err)
	}
}
