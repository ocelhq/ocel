package topic_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/devresources/topic"
	"github.com/ocelhq/ocel/cli/internal/project"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
)

const liveEnv = "OCEL_LIVE_DOCKER"

type said struct {
	mu    sync.Mutex
	lines []string
}

func (s *said) say(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line)
}

func (s *said) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.lines)
}

type liveProject struct {
	cfg      *project.Project
	name     string
	stateDir string
	source   string
}

func newLiveProject(t *testing.T, name string) liveProject {
	t.Helper()
	if os.Getenv(liveEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveEnv)
	}
	dir := t.TempDir()
	t.Cleanup(func() {
		ctx := context.Background()
		engine, err := docker.Open(ctx)
		if err != nil {
			return
		}
		_ = engine.Wipe(ctx, docker.ProjectLabels(name))
		_ = engine.Close()
	})
	return liveProject{
		cfg:      &project.Project{Dir: dir, Slug: name, Apps: []project.App{{Name: "web", Path: "."}}},
		name:     name,
		stateDir: t.TempDir(),
		source:   filepath.Join(dir, "jobs", "index.ts") + ":3",
	}
}

func (p liveProject) greet() declaration.Resource {
	return declaration.Resource{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "greet", Task: &resourcesv1.TaskConfig{}, Source: p.source}
}

func (p liveProject) backend(t *testing.T, report func(string)) *topic.Backend {
	t.Helper()
	backend := topic.New(docker.Open, p.stateDir, p.cfg, report)
	t.Cleanup(func() { _ = backend.Close(context.Background(), true) })
	return backend
}

func tasksOf(t *testing.T, backend *topic.Backend) taskv1connect.TaskServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	backend.Routes(mux, func(next http.Handler) http.Handler { return next })
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return taskv1connect.NewTaskServiceClient(server.Client(), server.URL)
}

func awaitStatus(t *testing.T, tasks taskv1connect.TaskServiceClient, id string, want taskv1.RunStatus) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := tasks.RetrieveRun(context.Background(), &taskv1.RetrieveRunRequest{Id: id})
		if err != nil {
			t.Fatalf("RetrieveRun(%s): %v", id, err)
		}
		if resp.GetRun().GetStatus() == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is %s, want %s", id, resp.GetRun().GetStatus(), want)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestDockerATriggeredTaskRunsOnTheWorkerItIsServedFromAndItsRunIsReported(t *testing.T) {
	live := newLiveProject(t, "topic-live-run-test")
	ctx := context.Background()
	reported := &said{}
	backend := live.backend(t, reported.say)

	resolved, err := backend.Resolve(ctx, live.name, []declaration.Resource{live.greet()})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(resolved) != 1 || resolved[0].Env["OCEL_RESOURCE_TASK_greet"] != `{"name":"greet","task":{}}` {
		t.Fatalf("resolved = %+v, want task greet bound as provisioned", resolved)
	}
	if workers := backend.Workers(); len(workers) != 1 || workers[0].Name != "worker" || !slices.Equal(workers[0].Sources, []string{live.source}) {
		t.Fatalf("workers = %+v, want the default worker, serving the task declared at %s", workers, live.source)
	}

	delivered := make(chan string, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		delivered <- string(body)
		_, _ = io.WriteString(w, `"hello ada"`)
	}))
	t.Cleanup(worker.Close)
	if err := backend.Serve(ctx, map[string]string{"worker": worker.URL}); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	tasks := tasksOf(t, backend)
	resp, err := tasks.Trigger(ctx, &taskv1.TriggerRequest{Task: "greet", Payload: []byte(`{"name":"ada"}`)})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	awaitStatus(t, tasks, resp.GetId(), taskv1.RunStatus_RUN_STATUS_COMPLETED)
	if envelope := <-delivered; !strings.Contains(envelope, `"name":"ada"`) {
		t.Errorf("the worker received %s, want the triggered payload", envelope)
	}
	want := `task "greet" run ` + resp.GetId() + ` completed`
	if lines := reported.all(); !slices.ContainsFunc(lines, func(line string) bool { return strings.HasPrefix(line, want) }) {
		t.Errorf("reported %q, want a line starting %q", lines, want)
	}
}

func TestDockerARunOutlivesTheDevSessionThatTriggeredIt(t *testing.T) {
	live := newLiveProject(t, "topic-live-restart-test")
	ctx := context.Background()

	first := topic.New(docker.Open, live.stateDir, live.cfg, func(string) {})
	if _, err := first.Resolve(ctx, live.name, []declaration.Resource{live.greet()}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	resp, err := tasksOf(t, first).Trigger(ctx, &taskv1.TriggerRequest{Task: "greet", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("Trigger: %v", err)
	}
	if err := first.Close(ctx, true); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := live.backend(t, func(string) {})
	if _, err := second.Resolve(ctx, live.name, []declaration.Resource{live.greet()}); err != nil {
		t.Fatalf("Resolve after a restart: %v", err)
	}
	awaitStatus(t, tasksOf(t, second), resp.GetId(), taskv1.RunStatus_RUN_STATUS_QUEUED)
}
