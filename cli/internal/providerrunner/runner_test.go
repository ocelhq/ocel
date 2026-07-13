package providerrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// spawnFake spawns this test binary re-exec'd as a fake provider in mode,
// with a fresh Unix socket path under t.TempDir(). It registers t.Cleanup to
// Close the runner, so tests don't need to remember teardown on every
// return path (including t.Fatal).
func spawnFake(t *testing.T, ctx context.Context, mode string, cfg Config) (*Runner, string) {
	t.Helper()

	sockPath := filepath.Join(t.TempDir(), "provider.sock")
	cfg.BinaryPath = os.Args[0]
	cfg.Env = append([]string{
		fakeProviderEnvVar + "=1",
		fakeProviderModeEnvVar + "=" + mode,
		fakeProviderSockEnvVar + "=" + sockPath,
	}, cfg.Env...)

	r, err := Spawn(ctx, cfg)
	if err != nil {
		t.Fatalf("Spawn() error = %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, sockPath
}

func TestHappyPath_ReadyDeploySuccess(t *testing.T) {
	ctx := context.Background()
	r, sockPath := spawnFake(t, ctx, "success", Config{})

	if err := r.Ready(ctx); err != nil {
		t.Fatalf("Ready() error = %v, want nil", err)
	}

	var events []*deploymentsv1.DeployEvent
	err := r.Deploy(ctx, &deploymentsv1.DeployRequest{
		Manifest:        &deploymentsv1.Manifest{SchemaVersion: "provider.v1"},
		Options:         []byte("{}"),
		ProtocolVersion: "provider.v1",
	}, func(ev *deploymentsv1.DeployEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("Deploy() error = %v, want nil", err)
	}

	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (progress, result)", len(events))
	}
	if events[0].GetProgress() == nil {
		t.Errorf("events[0] = %v, want a ProgressEvent", events[0])
	}
	result := events[1].GetResult()
	if result == nil || !result.GetSuccess() {
		t.Errorf("events[1] = %v, want a successful ResultEvent", events[1])
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertProcessGone(t, r)
	assertNoStaleSocket(t, sockPath)
}

func TestReady_ExitBeforeSentinel(t *testing.T) {
	ctx := context.Background()
	r, _ := spawnFake(t, ctx, "exit-before-ready", Config{ReadyTimeout: 5 * time.Second})

	start := time.Now()
	err := r.Ready(ctx)
	elapsed := time.Since(start)

	var earlyExit *EarlyExitError
	if !errors.As(err, &earlyExit) {
		t.Fatalf("Ready() error = %v (%T), want *EarlyExitError", err, err)
	}
	if earlyExit.Stderr == "" {
		t.Errorf("EarlyExitError.Stderr is empty, want the child's captured stderr")
	}
	if elapsed >= 5*time.Second {
		t.Errorf("Ready() took %s, want it to fail immediately rather than wait out the 5s timeout", elapsed)
	}

	// Distinct from the timeout and missing-binary cases.
	var timeoutErr *ReadyTimeoutError
	if errors.As(err, &timeoutErr) {
		t.Errorf("Ready() returned a *ReadyTimeoutError for an early exit")
	}
}

func TestReady_Timeout(t *testing.T) {
	ctx := context.Background()
	r, sockPath := spawnFake(t, ctx, "never-ready", Config{ReadyTimeout: 150 * time.Millisecond})

	err := r.Ready(ctx)

	var timeoutErr *ReadyTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("Ready() error = %v (%T), want *ReadyTimeoutError", err, err)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertProcessGone(t, r)
	assertNoStaleSocket(t, sockPath)
}

func TestSpawn_MissingBinary(t *testing.T) {
	_, err := Spawn(context.Background(), Config{BinaryPath: filepath.Join(t.TempDir(), "does-not-exist")})
	if err == nil {
		t.Fatal("Spawn() error = nil, want an error for a missing binary")
	}

	var earlyExit *EarlyExitError
	var timeoutErr *ReadyTimeoutError
	if errors.As(err, &earlyExit) || errors.As(err, &timeoutErr) {
		t.Errorf("Spawn() error = %v (%T), want a distinct missing-binary error, not EarlyExitError/ReadyTimeoutError", err, err)
	}
}

func TestDeploy_KilledMidCall(t *testing.T) {
	ctx := context.Background()
	r, sockPath := spawnFake(t, ctx, "hang-deploy", Config{})

	if err := r.Ready(ctx); err != nil {
		t.Fatalf("Ready() error = %v, want nil", err)
	}

	var gotFirstEvent atomic.Bool
	deployErrCh := make(chan error, 1)
	go func() {
		deployErrCh <- r.Deploy(ctx, &deploymentsv1.DeployRequest{
			Manifest: &deploymentsv1.Manifest{SchemaVersion: "provider.v1"},
			Options:  []byte("{}"),
		}, func(ev *deploymentsv1.DeployEvent) { gotFirstEvent.Store(true) })
	}()

	// Wait for the provider to be mid-call, then kill it out from under
	// Deploy, simulating e.g. an OOM kill or `kill -9` during a real
	// provisioning run.
	deadline := time.Now().Add(2 * time.Second)
	for !gotFirstEvent.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !gotFirstEvent.Load() {
		t.Fatal("never received the first DeployEvent before the kill deadline")
	}
	if err := r.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill provider process: %v", err)
	}

	select {
	case err := <-deployErrCh:
		if err == nil {
			t.Fatal("Deploy() error = nil, want an error after the provider was killed mid-call")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deploy() hung after the provider was killed mid-call")
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertNoStaleSocket(t, sockPath)
}

func TestDeploy_TerminalFailure(t *testing.T) {
	ctx := context.Background()
	r, _ := spawnFake(t, ctx, "fail", Config{})

	if err := r.Ready(ctx); err != nil {
		t.Fatalf("Ready() error = %v, want nil", err)
	}

	err := r.Deploy(ctx, &deploymentsv1.DeployRequest{Manifest: &deploymentsv1.Manifest{}, Options: []byte("{}")}, nil)

	var deployErr *DeployFailedError
	if !errors.As(err, &deployErr) {
		t.Fatalf("Deploy() error = %v (%T), want *DeployFailedError", err, err)
	}
	if deployErr.Message != "simulated deploy failure" {
		t.Errorf("DeployFailedError.Message = %q, want %q", deployErr.Message, "simulated deploy failure")
	}
}

func TestBootstrap_Success(t *testing.T) {
	ctx := context.Background()
	r, sockPath := spawnFake(t, ctx, "success", Config{})

	if err := r.Ready(ctx); err != nil {
		t.Fatalf("Ready() error = %v, want nil", err)
	}

	var events []*deploymentsv1.DeployEvent
	err := r.Bootstrap(ctx, &deploymentsv1.BootstrapRequest{
		Options:         []byte("{}"),
		ProtocolVersion: "provider.v1",
	}, func(ev *deploymentsv1.DeployEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("Bootstrap() error = %v, want nil", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (progress, result)", len(events))
	}
	if result := events[1].GetResult(); result == nil || !result.GetSuccess() {
		t.Errorf("events[1] = %v, want a successful ResultEvent", events[1])
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	assertProcessGone(t, r)
	assertNoStaleSocket(t, sockPath)
}

func TestBootstrap_TerminalFailure(t *testing.T) {
	ctx := context.Background()
	r, _ := spawnFake(t, ctx, "fail", Config{})

	if err := r.Ready(ctx); err != nil {
		t.Fatalf("Ready() error = %v, want nil", err)
	}

	err := r.Bootstrap(ctx, &deploymentsv1.BootstrapRequest{Options: []byte("{}")}, nil)

	var failErr *DeployFailedError
	if !errors.As(err, &failErr) {
		t.Fatalf("Bootstrap() error = %v (%T), want *DeployFailedError", err, err)
	}
	if failErr.Message != "simulated bootstrap failure" {
		t.Errorf("DeployFailedError.Message = %q, want %q", failErr.Message, "simulated bootstrap failure")
	}
}

func TestClose_NoOrphanOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, sockPath := spawnFake(t, ctx, "success", Config{})

	if err := r.Ready(ctx); err != nil {
		t.Fatalf("Ready() error = %v, want nil", err)
	}

	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case <-r.done:
		default:
			if time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			t.Fatal("provider process still running 2s after ctx cancellation")
		}
		break
	}

	assertNoStaleSocket(t, sockPath)
}

func TestClose_Idempotent(t *testing.T) {
	ctx := context.Background()
	r, _ := spawnFake(t, ctx, "success", Config{})
	if err := r.Ready(ctx); err != nil {
		t.Fatalf("Ready() error = %v, want nil", err)
	}

	if err := r.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

// assertProcessGone fails the test if r's process is still alive.
func assertProcessGone(t *testing.T, r *Runner) {
	t.Helper()
	select {
	case <-r.done:
	default:
		t.Error("provider process is still running after Close()")
	}
}

// assertNoStaleSocket fails the test if sockPath still exists on disk.
func assertNoStaleSocket(t *testing.T, sockPath string) {
	t.Helper()
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("stale socket file left behind at %s (stat err = %v)", sockPath, err)
	}
}

func init() {
	// Fail fast with a clear message if this test binary can't re-exec
	// itself, rather than every test in the file timing out mysteriously.
	if _, err := os.Stat(os.Args[0]); err != nil {
		panic(fmt.Sprintf("providerrunner tests require os.Args[0] to be a runnable test binary: %v", err))
	}
}
