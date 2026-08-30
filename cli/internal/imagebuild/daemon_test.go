package imagebuild_test

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
)

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

func standIn(t *testing.T, answer func(http.ResponseWriter, *http.Request)) *asked {
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
	t.Setenv(imagebuild.DockerHostEnv, "unix://"+socket)
	return seen
}

func upgrades(w http.ResponseWriter, _ *http.Request) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: h2c\r\n\r\n"))
}

func TestTheBuilderIsReachedByUpgradingTheDaemonSocketRatherThanDiallingIt(t *testing.T) {
	seen := standIn(t, upgrades)

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
		t.Errorf("the request carries Connection: %q, want Upgrade, and the daemon never hands the connection over", got)
	}
}

func TestADaemonThatServesNoBuilderIsRefusedWithTheAnswerItGave(t *testing.T) {
	standIn(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })

	err := imagebuild.Reachable(context.Background())
	if err == nil {
		t.Fatal("Reachable() over a daemon that answers 404 succeeded, so the build would be attempted against nothing")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("Reachable() = %v, and the reason never says what the daemon answered", err)
	}
}

func TestNoDaemonAtAllNamesTheVariableThatPointsAtOne(t *testing.T) {
	t.Setenv(imagebuild.DockerHostEnv, "unix://"+filepath.Join(t.TempDir(), "absent.sock"))

	err := imagebuild.Reachable(context.Background())
	if err == nil {
		t.Fatal("Reachable() with no daemon behind the socket succeeded")
	}
	if !strings.Contains(err.Error(), imagebuild.DockerHostEnv) {
		t.Errorf("Reachable() = %v, and the reader is never told which variable points ocel at a daemon", err)
	}
}

func TestASchemeOcelCannotDialIsRefusedBeforeAnythingIsDialled(t *testing.T) {
	t.Setenv(imagebuild.DockerHostEnv, "ssh://ubuntu@build-box")

	err := imagebuild.Reachable(context.Background())
	if err == nil {
		t.Fatal("Reachable() over a scheme ocel cannot dial succeeded")
	}
	if !strings.Contains(err.Error(), imagebuild.DockerHostEnv) || !strings.Contains(err.Error(), "ssh://ubuntu@build-box") {
		t.Errorf("Reachable() = %v, want the variable and the value it was given", err)
	}
}
