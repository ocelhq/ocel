package docker_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
)

type fakeDaemon struct {
	mu      sync.Mutex
	held    map[string]map[string]any
	calls   []string
	created []map[string]any
	onStart func()
	racedBy map[string]any
}

var versionPrefix = regexp.MustCompile(`^/v[0-9.]+`)

func (f *fakeDaemon) saw(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.calls, call)
}

func (f *fakeDaemon) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+path)
	f.mu.Unlock()

	w.Header().Set("Api-Version", "1.47")
	w.Header().Set("Content-Type", "application/json")
	switch {
	case path == "/_ping":
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(path, "/images/"):
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:held"})
	case r.Method == http.MethodPost && path == "/containers/create":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		if f.racedBy != nil {
			f.held[r.URL.Query().Get("name")] = f.racedBy
			f.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Conflict. The container name is already in use"})
			return
		}
		f.created = append(f.created, body)
		config := map[string]any{"Image": body["Image"], "Labels": body["Labels"]}
		f.held["created"] = inspected("created", true, config)
		f.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "created"})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/start"):
		if f.onStart != nil {
			f.onStart()
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/stop"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodDelete:
		f.mu.Lock()
		delete(f.held, strings.TrimPrefix(path, "/containers/"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/json"):
		f.mu.Lock()
		found, held := f.held[strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")]
		f.mu.Unlock()
		if !held {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "No such container"})
			return
		}
		_ = json.NewEncoder(w).Encode(found)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func inspected(id string, running bool, config map[string]any) map[string]any {
	return map[string]any{
		"Id":     id,
		"State":  map[string]any{"Running": running},
		"Config": config,
		"NetworkSettings": map[string]any{"Ports": map[string]any{
			"5432/tcp": []map[string]any{{"HostIp": "127.0.0.1", "HostPort": "49153"}},
		}},
	}
}

func openFake(t *testing.T, daemon *fakeDaemon) docker.Engine {
	t.Helper()
	if daemon.held == nil {
		daemon.held = map[string]map[string]any{}
	}
	server := httptest.NewServer(daemon)
	t.Cleanup(server.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(server.URL, "http://"))
	engine, err := docker.Open(context.Background())
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

var spec = docker.Spec{
	Name:   "ocel-dev-shop-postgres-17",
	Image:  "postgres:17@sha256:abc",
	Port:   5432,
	Labels: docker.Labels("shop", "postgres"),
}

func TestOpenRefusesADockerHostItCannotUseTheWayItRefusesADeadOne(t *testing.T) {
	t.Setenv("DOCKER_HOST", "nowhere")

	_, err := docker.Open(context.Background())

	var unreachable *docker.Unreachable
	if !errors.As(err, &unreachable) {
		t.Fatalf("Open = %v, want an Unreachable the stack can name a resource in", err)
	}
	if unreachable.Address != "nowhere" {
		t.Errorf("Address = %q, want the host that was asked for", unreachable.Address)
	}
}

func TestRunStartsWhatIsNotThereOnLoopback(t *testing.T) {
	daemon := &fakeDaemon{}
	engine := openFake(t, daemon)

	running, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if running.ID != "created" || running.Addr != "127.0.0.1:49153" {
		t.Fatalf("Run = %+v, want the created container on the loopback port docker chose", running)
	}
	bindings, _ := json.Marshal(daemon.created[0]["HostConfig"].(map[string]any)["PortBindings"])
	if !strings.Contains(string(bindings), `"HostIp":"127.0.0.1"`) {
		t.Errorf("PortBindings = %s, want the port published on loopback only", bindings)
	}
}

func TestRunAdoptsARunningContainerOfTheSameSpec(t *testing.T) {
	daemon := &fakeDaemon{held: map[string]map[string]any{
		spec.Name: inspected("theirs", true, map[string]any{"Image": spec.Image, "Labels": spec.Labels}),
	}}
	engine := openFake(t, daemon)

	running, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if running.ID != "theirs" || running.Addr != "127.0.0.1:49153" {
		t.Fatalf("Run = %+v, want the container another process already runs", running)
	}
	if len(daemon.created) != 0 || daemon.saw("DELETE /containers/"+spec.Name) {
		t.Fatalf("calls = %v, want nothing created and nothing removed", daemon.calls)
	}
}

func TestRunRefusesToReplaceARunningContainerOfAnotherSpec(t *testing.T) {
	daemon := &fakeDaemon{held: map[string]map[string]any{
		spec.Name: inspected("theirs", true, map[string]any{"Image": "postgres:16", "Labels": spec.Labels}),
	}}
	engine := openFake(t, daemon)

	_, err := engine.Run(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), spec.Name) || !strings.Contains(err.Error(), "docker rm -f") {
		t.Fatalf("Run = %v, want a refusal naming the container and how to clear it", err)
	}
	for _, call := range daemon.calls {
		if strings.HasPrefix(call, "DELETE") || strings.HasSuffix(call, "/stop") {
			t.Fatalf("calls = %v, want a running container left alone", daemon.calls)
		}
	}
}

func TestRunReplacesAContainerThatIsNoLongerRunning(t *testing.T) {
	daemon := &fakeDaemon{held: map[string]map[string]any{
		spec.Name: inspected("dead", false, map[string]any{"Image": spec.Image, "Labels": spec.Labels}),
	}}
	engine := openFake(t, daemon)

	running, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if running.ID != "created" || !daemon.saw("DELETE /containers/dead") {
		t.Fatalf("Run = %+v after %v, want the dead container removed and a new one started", running, daemon.calls)
	}
}

func TestAnInterruptedStartStillRemovesTheContainerItCreated(t *testing.T) {
	ctx, interrupt := context.WithCancel(context.Background())
	daemon := &fakeDaemon{onStart: interrupt}
	engine := openFake(t, daemon)

	if _, err := engine.Run(ctx, spec); err == nil {
		t.Fatal("Run = nil for a start that was interrupted")
	}
	if !daemon.saw("DELETE /containers/created") {
		t.Fatalf("calls = %v, want the created container removed after the interrupt", daemon.calls)
	}
}

func TestRunAdoptsTheContainerAnotherProcessCreatedFirst(t *testing.T) {
	daemon := &fakeDaemon{racedBy: inspected("theirs", true, map[string]any{"Image": spec.Image, "Labels": spec.Labels})}
	engine := openFake(t, daemon)

	running, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if running.ID != "theirs" {
		t.Fatalf("Run = %+v, want the container that won the name", running)
	}
}
