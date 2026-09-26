package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/envvars"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
	source "github.com/ocelhq/ocel/platform/vps/runtime/live"
)

const containerID = "021294b7a2a44cb5a12500a19d9fa7842f10fae35f15cfe25105844c754253d8"

func TestACallerIsKnownByTheContainerItsCgroupNamesUnderEitherDriver(t *testing.T) {
	t.Parallel()
	for name, held := range map[string]struct {
		cgroup string
		want   string
	}{
		"cgroup v2 under the systemd driver":  {"0::/system.slice/docker-" + containerID + ".scope\n", containerID},
		"cgroup v2 under the cgroupfs driver": {"0::/docker/" + containerID + "\n", containerID},
		"cgroup v1 under the systemd driver":  {"12:memory:/system.slice/docker-" + containerID + ".scope\n11:cpu:/system.slice/docker-" + containerID + ".scope\n", containerID},
		"a process outside every container":   {"0::/user.slice/user-1000.slice/session-3.scope\n", ""},
		"a scope named after a short id":      {"0::/system.slice/docker-021294b7a2a4.scope\n", ""},
		"a scope that is not the engine's":    {"0::/system.slice/podman-" + containerID + ".scope\n", ""},
	} {
		t.Run(name, func(t *testing.T) {
			got, found := ContainerID(held.cgroup)
			if got != held.want || found != (held.want != "") {
				t.Errorf("ContainerID(%q) = %q, %v, want %q", held.cgroup, got, found, held.want)
			}
		})
	}
}

type inspecting struct {
	mu        sync.Mutex
	manifests map[string]string
	asked     []string
	broken    error
}

func (i *inspecting) Manifest(_ context.Context, container string) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.asked = append(i.asked, container)
	if i.broken != nil {
		return "", i.broken
	}
	return i.manifests[container], nil
}

type resolving struct {
	mu    sync.Mutex
	given []vars.Manifest
}

func (r *resolving) Resolve(_ context.Context, manifest vars.Manifest) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.given = append(r.given, manifest)
	return map[string]string{"DATABASE_URL": "postgres://" + manifest.Slug + "/" + manifest.Environment}, nil
}

func procNaming(t *testing.T, cgroup string) string {
	t.Helper()
	proc := t.TempDir()
	dir := filepath.Join(proc, strconv.Itoa(os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if cgroup == "" {
		return proc
	}
	if err := os.WriteFile(filepath.Join(dir, "cgroup"), []byte(cgroup), 0o644); err != nil {
		t.Fatal(err)
	}
	return proc
}

func serving(t *testing.T, server *Server) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ocel-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, vars.SocketFile)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, ln) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("Serve() = %v", err)
		}
	})
	return socket
}

func ask(t *testing.T, socket string) (int, string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	resp, err := client.Get("http://ocel-live" + vars.ValuesPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func manifestFor(t *testing.T, slug, environment string) string {
	t.Helper()
	rendered, err := vars.Render(vars.Manifest{Slug: slug, Class: "production", Environment: environment, Keys: []live.Key{{Key: "DATABASE_URL"}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(rendered)
}

func TestTheAgentAnswersACallerWithTheValuesItsOwnContainerWasHandedAndNothingItSays(t *testing.T) {
	t.Parallel()
	inspect := &inspecting{manifests: map[string]string{containerID: manifestFor(t, "shop", "pr-7")}}
	resolve := &resolving{}
	socket := serving(t, &Server{
		Proc:    procNaming(t, "0::/system.slice/docker-"+containerID+".scope\n"),
		Inspect: inspect,
		Resolve: resolve,
	})

	status, body := ask(t, socket)
	if status != http.StatusOK {
		t.Fatalf("the agent answered %d: %s", status, body)
	}
	var answer vars.Answer
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("the agent answered %q, which is no value set: %v", body, err)
	}
	if answer.Values["DATABASE_URL"] != "postgres://shop/pr-7" {
		t.Errorf("the agent answered %v, want the value resolved under the caller's own manifest", answer.Values)
	}
	if len(inspect.asked) != 1 || inspect.asked[0] != containerID {
		t.Errorf("the agent asked the engine about %v, want the one container the caller's cgroup names", inspect.asked)
	}
	if len(resolve.given) != 1 || resolve.given[0].Slug != "shop" || resolve.given[0].Environment != "pr-7" {
		t.Errorf("the agent resolved %+v, want the manifest the engine holds for the caller's container: nothing the caller sends names a scope", resolve.given)
	}

	over := source.Over(vars.Manifest{Slug: "shop", Class: "production", Keys: []live.Key{{Key: "DATABASE_URL"}}}, socket)
	if err := over.Join(over.Prefetch(context.Background())); err != nil {
		t.Fatalf("the runtime's own fetcher could not read through the socket: %v", err)
	}
	if missing := over.Missing(); len(missing) != 0 {
		t.Errorf("the runtime read %v as missing after a fetch that answered it", missing)
	}
}

type measuring struct {
	mu     sync.Mutex
	asked  []string
	free   uint64
	total  uint64
	broken error
}

func (m *measuring) Space(_ context.Context, volume string) (uint64, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asked = append(m.asked, volume)
	return m.free, m.total, m.broken
}

func storeManifest(t *testing.T, volume string) string {
	t.Helper()
	rendered, err := vars.Render(vars.Manifest{
		Slug: "shop", Class: "production", Keys: []live.Key{{Key: "DATABASE_URL"}},
		Store: &vars.Store{Env: "shop-prod", Volume: volume},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(rendered)
}

func askSpace(t *testing.T, socket string) (int, string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	resp, err := client.Get("http://ocel-live" + vars.SpacePath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestTheAgentMeasuresTheVolumeTheCallersOwnManifestNamesAndNoOther(t *testing.T) {
	t.Parallel()
	space := &measuring{free: 7 << 30, total: 40 << 30}
	socket := serving(t, &Server{
		Proc:    procNaming(t, "0::/system.slice/docker-"+containerID+".scope\n"),
		Inspect: &inspecting{manifests: map[string]string{containerID: storeManifest(t, "shop-prod-store-s3-data")}},
		Resolve: &resolving{},
		Space:   space,
	})

	status, body := askSpace(t, socket)
	if status != http.StatusOK {
		t.Fatalf("the agent answered %d asking after the store volume: %s", status, body)
	}
	var answer vars.Space
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("the agent answered %q, which is no measurement: %v", body, err)
	}
	if answer.Free != 7<<30 || answer.Total != 40<<30 {
		t.Errorf("the agent answered %+v, want what the store's volume holds", answer)
	}
	if len(space.asked) != 1 || space.asked[0] != "shop-prod-store-s3-data" {
		t.Errorf("the agent measured %v, want the volume the caller's own manifest names", space.asked)
	}
}

func TestACallerWhoseManifestNamesNoStoreIsToldOfNoVolume(t *testing.T) {
	t.Parallel()
	space := &measuring{free: 1, total: 2}
	socket := serving(t, &Server{
		Proc:    procNaming(t, "0::/system.slice/docker-"+containerID+".scope\n"),
		Inspect: &inspecting{manifests: map[string]string{containerID: manifestFor(t, "shop", "")}},
		Resolve: &resolving{},
		Space:   space,
	})
	status, body := askSpace(t, socket)
	if status != http.StatusNotFound {
		t.Errorf("the agent answered %d %q, want a refusal: a container with no store of its own measures nothing", status, body)
	}
	if len(space.asked) != 0 {
		t.Errorf("the agent measured %v for a caller whose manifest names no store volume", space.asked)
	}
}

func TestACallerOutsideEveryContainerIsRefused(t *testing.T) {
	t.Parallel()
	inspect := &inspecting{manifests: map[string]string{containerID: manifestFor(t, "shop", "")}}
	socket := serving(t, &Server{Proc: procNaming(t, "0::/user.slice/user-1000.slice/session-3.scope\n"), Inspect: inspect, Resolve: &resolving{}})

	status, body := ask(t, socket)
	if status != http.StatusForbidden {
		t.Errorf("a caller on the box itself was answered %d %q, want a refusal: the agent hands a value to a container's own process and nobody else", status, body)
	}
	if len(inspect.asked) != 0 {
		t.Errorf("the agent asked the engine about %v for a caller no cgroup places in a container", inspect.asked)
	}
}

func TestACallerWhoseCgroupCannotBeReadIsRefused(t *testing.T) {
	t.Parallel()
	socket := serving(t, &Server{Proc: procNaming(t, ""), Inspect: &inspecting{}, Resolve: &resolving{}})
	if status, body := ask(t, socket); status != http.StatusForbidden {
		t.Errorf("a caller whose cgroup is unreadable was answered %d %q", status, body)
	}
}

func TestAContainerHandedNoManifestIsToldSoRatherThanHandedAnything(t *testing.T) {
	t.Parallel()
	resolve := &resolving{}
	socket := serving(t, &Server{
		Proc:    procNaming(t, "0::/docker/"+containerID+"\n"),
		Inspect: &inspecting{manifests: map[string]string{}},
		Resolve: resolve,
	})
	if status, body := ask(t, socket); status != http.StatusNotFound {
		t.Errorf("a container carrying no manifest was answered %d %q", status, body)
	}
	if len(resolve.given) != 0 {
		t.Errorf("the agent resolved %+v for a container that declared nothing live", resolve.given)
	}
}

func TestAnEngineThatCannotBeAskedIsReportedAsTheBoxsFault(t *testing.T) {
	t.Parallel()
	socket := serving(t, &Server{
		Proc:    procNaming(t, "0::/docker/"+containerID+"\n"),
		Inspect: &inspecting{broken: errors.New("the daemon is not answering")},
		Resolve: &resolving{},
	})
	if status, body := ask(t, socket); status != http.StatusBadGateway || !strings.Contains(body, "not answering") {
		t.Errorf("an engine that cannot be asked was answered %d %q, want 502 saying why", status, body)
	}
}

func engineOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("this machine carries no docker, so no container can ask the agent for anything")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("the docker on this machine answers nothing, so no container can ask the agent for anything")
	}
}

func underHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("this machine has no home directory to hand the engine a bind source from")
	}
	dir, err := os.MkdirTemp(home, "ocel-live-")
	if err != nil {
		t.Skip("nothing under this home directory can be written, so the engine can be handed no bind source")
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func containerRuntime(t *testing.T, dir string) string {
	t.Helper()
	binary := filepath.Join(dir, "runtime")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "../cmd/container")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if said, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the container runtime: %v\n%s", err, said)
	}
	return binary
}

const testImage = "public.ecr.aws/docker/library/alpine:3.21"

func pulled(t *testing.T, image string) {
	t.Helper()
	if exec.Command("docker", "image", "inspect", image).Run() == nil {
		return
	}
	var said []byte
	var err error
	for attempt := range 6 {
		if said, err = exec.Command("docker", "pull", "--quiet", image).CombinedOutput(); err == nil {
			return
		}
		if !strings.Contains(strings.ToLower(string(said)), "toomanyrequests") {
			break
		}
		time.Sleep(time.Second<<attempt + rand.N(time.Second))
	}
	t.Fatalf("pull %s: %v\n%s", image, err, said)
}

func TestAContainerReadsItsSecretOffTheBoxThroughTheRuntimeAndTheAgent(t *testing.T) {
	engineOrSkip(t)
	pulled(t, testImage)
	dir := underHome(t)
	runtimeBinary := containerRuntime(t, dir)

	b := aBox(t, dir)
	b.set(t, shop, envvars.Coordinate{Cell: envvars.Cell{Key: "DATABASE_URL"}}, "postgres://app:hunter2@db.internal/orders")
	b.dump(t)
	inspect, err := NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	socketDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(socketDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(socketDir, vars.SocketFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(socketDir, vars.SocketFile), 0o666); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- (&Server{Inspect: inspect, Resolve: b.resolver()}).Serve(ctx, ln) }()
	t.Cleanup(func() {
		stop()
		if err := <-served; err != nil {
			t.Errorf("Serve() = %v", err)
		}
	})

	manifest := manifestFor(t, "shop", "")
	run := exec.Command("docker", "run", "--rm", "--pull", "never", "--network", "none",
		"--mount", "type=bind,src="+socketDir+",dst="+vars.SocketDir+",readonly",
		"--volume", runtimeBinary+":"+providerkit.ContainerRuntimePath+":ro",
		"--env", vars.EnvVar+"="+manifest,
		"--env", providerkit.InjectedPortName+"="+providerkit.InjectedPortText,
		testImage, providerkit.ContainerRuntimePath,
		"sh", "-c", `cat "$OCEL_LIVE_DIR/DATABASE_URL"; echo; echo "keys=$OCEL_LIVE_KEYS"`)
	run.WaitDelay = 2 * time.Minute
	said, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the container did not run behind the runtime: %v\n%s", err, said)
	}
	if !strings.Contains(string(said), "postgres://app:hunter2@db.internal/orders") {
		t.Errorf("the app read %q off its live directory, want the secret sealed on the box", said)
	}
	if !strings.Contains(string(said), "keys=DATABASE_URL") {
		t.Errorf("the app was told %q, want the live keys named", said)
	}

}
