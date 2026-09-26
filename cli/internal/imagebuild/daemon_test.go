package imagebuild_test

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	control "github.com/moby/buildkit/api/services/control"
	types "github.com/moby/buildkit/api/types"
	"github.com/moby/buildkit/solver/pb"
	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"google.golang.org/grpc"
)

const snapshotterLabel = "org.mobyproject.buildkit.worker.snapshotter"

type asked struct {
	mu      sync.Mutex
	method  string
	path    string
	headers http.Header
}

func (a *asked) record(r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.method, a.path, a.headers = r.Method, r.URL.Path, r.Header.Clone()
}

func (a *asked) read(t *testing.T) (string, string, http.Header) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.method, a.path, a.headers
}

func fakeDaemon(t *testing.T, answer func(http.ResponseWriter, *http.Request)) *asked {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	seen := &asked{}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.record(r)
		answer(w, r)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	t.Setenv(images.DockerHostEnv, "unix://"+socket)
	return seen
}

type builder struct {
	control.UnimplementedControlServer
	workers []*types.WorkerRecord
}

func (b *builder) ListWorkers(context.Context, *control.ListWorkersRequest) (*control.ListWorkersResponse, error) {
	return &control.ListWorkersResponse{Record: b.workers}, nil
}

type handover struct {
	mu     sync.Mutex
	conn   net.Conn
	closed chan struct{}
	once   sync.Once
}

func (h *handover) Accept() (net.Conn, error) {
	h.mu.Lock()
	conn := h.conn
	h.conn = nil
	h.mu.Unlock()
	if conn != nil {
		return conn, nil
	}
	<-h.closed
	return nil, net.ErrClosed
}

func (h *handover) Close() error {
	h.once.Do(func() { close(h.closed) })
	return nil
}

func (h *handover) Addr() net.Addr { return &net.UnixAddr{Name: "docker", Net: "unix"} }

func servesBuilder(t *testing.T, workers ...*types.WorkerRecord) *asked {
	t.Helper()
	return fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		if _, err := conn.Write([]byte("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: h2c\r\n\r\n")); err != nil {
			_ = conn.Close()
			return
		}
		server := grpc.NewServer()
		control.RegisterControlServer(server, &builder{workers: workers})
		t.Cleanup(server.Stop)
		_ = server.Serve(&handover{conn: conn, closed: make(chan struct{})})
	})
}

func containerd() *types.WorkerRecord {
	return &types.WorkerRecord{ID: "containerd", Labels: map[string]string{snapshotterLabel: "overlayfs"}}
}

func containerdBuildingFor(arches ...string) *types.WorkerRecord {
	worker := containerd()
	for _, arch := range arches {
		worker.Platforms = append(worker.Platforms, &pb.Platform{OS: "linux", Architecture: arch})
	}
	return worker
}

func classic() *types.WorkerRecord {
	return &types.WorkerRecord{ID: "classic", Labels: map[string]string{"org.mobyproject.buildkit.worker.executor": "oci"}}
}

func TestTheBuilderIsReachedByUpgradingTheDaemonSocketRatherThanDiallingIt(t *testing.T) {
	seen := servesBuilder(t, containerd())

	if err := imagebuild.Reachable(context.Background()); err != nil {
		t.Fatalf("Reachable() = %v, want the handshake a daemon that serves a builder answers", err)
	}

	method, path, headers := seen.read(t)
	if method != http.MethodPost || path != "/grpc" {
		t.Errorf("ocel asked the daemon %s %s, want POST /grpc, the endpoint its builder is behind", method, path)
	}
	if got := headers.Get("Upgrade"); got != "h2c" {
		t.Errorf("the request upgrades to %q, want %q, so the daemon answers raw gRPC rather than HTTP", got, "h2c")
	}
	if got := headers.Get("Connection"); got != "Upgrade" {
		t.Errorf("the request sends Connection: %q, want Upgrade, and the daemon never hands the connection over", got)
	}
}

func TestADaemonKeepingImagesInTheClassicStoreIsRefusedAtPreflight(t *testing.T) {
	servesBuilder(t, classic())

	err := imagebuild.Reachable(context.Background())
	if err == nil {
		t.Fatal("Reachable() passed a daemon whose store addresses no image by its digest, so the refusal lands after the user has consented to a bootstrap")
	}
	if !strings.Contains(err.Error(), "containerd") {
		t.Errorf("Reachable() = %v, and the reader is never told which image store to turn on", err)
	}
}

func TestOneWorkerWithTheContainerdStoreIsEnoughToBuildOn(t *testing.T) {
	servesBuilder(t, classic(), containerd())

	if err := imagebuild.Reachable(context.Background()); err != nil {
		t.Fatalf("Reachable() = %v, want the daemon accepted on the worker whose store either builder can be exported into", err)
	}
}

func TestADaemonThatCannotBuildForTheTargetsArchitectureIsRefusedAtPreflight(t *testing.T) {
	servesBuilder(t, containerdBuildingFor("arm64"))

	err := imagebuild.Reachable(context.Background(), "amd64")
	if err == nil {
		t.Fatal("Reachable() passed a daemon with no way to build for the target, so the refusal lands mid-build, after the user has consented to a bootstrap")
	}
	for _, named := range []string{"linux/arm64", "linux/amd64", "binfmt"} {
		if !strings.Contains(err.Error(), named) {
			t.Errorf("Reachable() = %v, and the reader is never told %s", err, named)
		}
	}
}

func TestADaemonEmulatingTheTargetsArchitectureIsEnoughToBuildOn(t *testing.T) {
	servesBuilder(t, containerdBuildingFor("arm64", "amd64"))

	if err := imagebuild.Reachable(context.Background(), "amd64"); err != nil {
		t.Fatalf("Reachable() = %v, want the daemon accepted: its builder names the target's architecture among those it runs", err)
	}
}

func TestEveryArchitectureTheProjectsContainersRunOnMustBeOneTheDaemonBuildsFor(t *testing.T) {
	servesBuilder(t, containerdBuildingFor("amd64"))

	err := imagebuild.Reachable(context.Background(), "amd64", "arm64")
	if err == nil || !strings.Contains(err.Error(), "linux/arm64") {
		t.Fatalf("Reachable() = %v, want the daemon refused for the arm64 app it cannot build, however many others it can", err)
	}
}

func TestAPlatformOnlyTheClassicStoresWorkerBuildsForIsStillRefused(t *testing.T) {
	onClassic := classic()
	onClassic.Platforms = []*pb.Platform{{OS: "linux", Architecture: "amd64"}}
	servesBuilder(t, onClassic, containerdBuildingFor("arm64"))

	if err := imagebuild.Reachable(context.Background(), "amd64"); err == nil {
		t.Fatal("Reachable() passed on a worker whose store addresses no image by digest, and the build cannot be exported from it")
	}
}

func TestABuildForThisMachinesOwnArchitectureAsksNothingOfTheWorkersPlatforms(t *testing.T) {
	servesBuilder(t, containerd())

	if err := imagebuild.Reachable(context.Background()); err != nil {
		t.Fatalf("Reachable() = %v, want a build pinned to nothing accepted on any worker it can export from", err)
	}
}

func TestADaemonWithNoWorkerAtAllIsRefusedAtPreflight(t *testing.T) {
	servesBuilder(t)

	if err := imagebuild.Reachable(context.Background()); err == nil {
		t.Fatal("Reachable() passed a daemon that named no worker, so the build has nothing to run on")
	}
}

func TestADaemonThatServesNoBuilderIsRefusedWithTheAnswerItGave(t *testing.T) {
	fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	err := imagebuild.Reachable(context.Background())
	if err == nil {
		t.Fatal("Reachable() over a daemon that answers 404 succeeded, so the build would be attempted against nothing")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("Reachable() = %v, and the reason never says what the daemon answered", err)
	}
}

func TestNoDaemonAtAllNamesTheVariableThatPointsAtOne(t *testing.T) {
	t.Setenv(images.DockerHostEnv, "unix://"+filepath.Join(t.TempDir(), "absent.sock"))

	err := imagebuild.Reachable(context.Background())
	if err == nil {
		t.Fatal("Reachable() with no daemon behind the socket succeeded")
	}
	if !strings.Contains(err.Error(), images.DockerHostEnv) {
		t.Errorf("Reachable() = %v, and the reader is never told which variable points ocel at a daemon", err)
	}
}

func TestASchemeOcelCannotDialIsRefusedBeforeAnythingIsDialled(t *testing.T) {
	t.Setenv(images.DockerHostEnv, "ssh://ubuntu@build-box")

	err := imagebuild.Reachable(context.Background())
	if err == nil {
		t.Fatal("Reachable() over a scheme ocel cannot dial succeeded")
	}
	if !strings.Contains(err.Error(), images.DockerHostEnv) || !strings.Contains(err.Error(), "ssh://ubuntu@build-box") {
		t.Errorf("Reachable() = %v, want the variable and the value it was given", err)
	}
}
