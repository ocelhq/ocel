package localharness

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

// TestHelperProcess is not a real test: it's re-exec'd as a plain OS process
// by the Spawn tests below (mirroring the pattern os/exec's own tests use),
// so Spawn can be exercised against a real process without depending on
// bun. It's a no-op under a normal `go test` run.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "TestHelperProcess: no mode given after --")
		os.Exit(2)
	}

	switch mode := args[1]; mode {
	case "exit-immediately":
		os.Exit(1)
	case "healthy":
		serveHealth(true)
	case "unhealthy":
		serveHealth(false)
	default:
		fmt.Fprintf(os.Stderr, "TestHelperProcess: unknown mode %q\n", mode)
		os.Exit(2)
	}
}

func serveHealth(healthy bool) {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if healthy {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	if err := http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func helperSpawnConfig(t *testing.T, mode string, startTimeout time.Duration) SpawnConfig {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return SpawnConfig{
		Command:      exe,
		Args:         []string{"-test.run=TestHelperProcess", "--", mode},
		Env:          append(os.Environ(), "GO_WANT_HELPER_PROCESS=1"),
		StartTimeout: startTimeout,
	}
}

func TestSpawn_WaitsForHealthCheckThenSucceeds(t *testing.T) {
	p, err := Spawn(context.Background(), helperSpawnConfig(t, "healthy", 5*time.Second))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	defer p.Stop()

	resp, err := http.Get("http://" + p.Addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want 200", resp.StatusCode)
	}
}

func TestSpawn_FailsFastWhenProcessExitsBeforeHealthy(t *testing.T) {
	start := time.Now()
	_, err := Spawn(context.Background(), helperSpawnConfig(t, "exit-immediately", 5*time.Second))
	if err == nil {
		t.Fatal("Spawn: expected error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Spawn: took %s to fail; want fail-fast well under the 5s StartTimeout", elapsed)
	}
}

func TestSpawn_FailsOnHealthCheckTimeout(t *testing.T) {
	p, err := Spawn(context.Background(), helperSpawnConfig(t, "unhealthy", 200*time.Millisecond))
	if err == nil {
		p.Stop()
		t.Fatal("Spawn: expected timeout error, got nil")
	}
}

func TestProcess_StopTerminatesProcess(t *testing.T) {
	p, err := Spawn(context.Background(), helperSpawnConfig(t, "healthy", 5*time.Second))
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	p.Stop()

	if _, err := http.Get("http://" + p.Addr + "/health"); err == nil {
		t.Fatal("GET /health after Stop: expected a connection error, got nil")
	}
}
