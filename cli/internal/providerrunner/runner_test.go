package providerrunner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/procgroup"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/progress/v1"
)

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
	t.Cleanup(r.Close)
	return r, sockPath
}

func TestSpawn(t *testing.T) {
	t.Parallel()

	t.Run("a missing binary fails with a distinct error, not a readiness one", func(t *testing.T) {
		t.Parallel()

		_, err := Spawn(context.Background(), Config{BinaryPath: filepath.Join(t.TempDir(), "does-not-exist")})
		if err == nil {
			t.Fatal("Spawn() error = nil, want an error for a missing binary")
		}

		var earlyExit *EarlyExitError
		var timeoutErr *ReadyTimeoutError
		if errors.As(err, &earlyExit) || errors.As(err, &timeoutErr) {
			t.Errorf("Spawn() error = %v (%T), want a distinct missing-binary error, not EarlyExitError/ReadyTimeoutError", err, err)
		}
	})
}

func TestReady(t *testing.T) {
	t.Parallel()

	t.Run("exiting before the sentinel fails immediately, carrying the child's stderr", func(t *testing.T) {
		t.Parallel()

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

		var timeoutErr *ReadyTimeoutError
		if errors.As(err, &timeoutErr) {
			t.Errorf("Ready() returned a *ReadyTimeoutError for an early exit")
		}
	})

	t.Run("a provider that never signals times out and leaves nothing behind", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, sockPath := spawnFake(t, ctx, "never-ready", Config{ReadyTimeout: 150 * time.Millisecond})

		err := r.Ready(ctx)

		var timeoutErr *ReadyTimeoutError
		if !errors.As(err, &timeoutErr) {
			t.Fatalf("Ready() error = %v (%T), want *ReadyTimeoutError", err, err)
		}

		r.Close()
		assertProcessGone(t, r)
		assertNoStaleSocket(t, sockPath)
	})

	t.Run("an unreadable stdout surfaces the scanner error instead of waiting out the timeout", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, _ := spawnFake(t, ctx, "oversized-line", Config{ReadyTimeout: 10 * time.Second})

		start := time.Now()
		err := r.Ready(ctx)
		elapsed := time.Since(start)

		if err == nil {
			t.Fatal("Ready() error = nil, want the dropped-scanner error")
		}
		var timeoutErr *ReadyTimeoutError
		if errors.As(err, &timeoutErr) {
			t.Fatalf("Ready() error = %v, want the scanner error rather than a ReadyTimeoutError", err)
		}
		if !strings.Contains(err.Error(), "read provider stdout") {
			t.Errorf("Ready() error = %q, want it to name the unreadable stdout", err)
		}
		if elapsed >= 10*time.Second {
			t.Errorf("Ready() took %s, want it to fail as soon as stdout became unreadable", elapsed)
		}
	})
}

func TestDeploy(t *testing.T) {
	t.Parallel()

	t.Run("a successful deploy streams progress then a result, and closing leaves nothing behind", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, sockPath := spawnFake(t, ctx, "success", Config{})

		if err := r.Ready(ctx); err != nil {
			t.Fatalf("Ready() error = %v, want nil", err)
		}

		var events []*progressv1.OperationEvent
		err := r.Deploy(ctx, &deploymentsv1.DeployRequest{
			Manifest: &deploymentsv1.Manifest{SchemaVersion: "provider.v1", Slug: "acme"},
		}, func(ev *progressv1.OperationEvent) { events = append(events, ev) })
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

		r.Close()
		assertProcessGone(t, r)
		assertNoStaleSocket(t, sockPath)
	})

	t.Run("killing the provider mid-call fails the call rather than hanging", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, sockPath := spawnFake(t, ctx, "hang-deploy", Config{})

		if err := r.Ready(ctx); err != nil {
			t.Fatalf("Ready() error = %v, want nil", err)
		}

		var gotFirstEvent atomic.Bool
		deployErrCh := make(chan error, 1)
		go func() {
			deployErrCh <- r.Deploy(ctx, &deploymentsv1.DeployRequest{
				Manifest: &deploymentsv1.Manifest{SchemaVersion: "provider.v1", Slug: "acme"},
			}, func(ev *progressv1.OperationEvent) { gotFirstEvent.Store(true) })
		}()

		deadline := time.Now().Add(2 * time.Second)
		for !gotFirstEvent.Load() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if !gotFirstEvent.Load() {
			t.Fatal("never received the first OperationEvent before the kill deadline")
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

		r.Close()
		assertNoStaleSocket(t, sockPath)
	})

	t.Run("a terminal failure carries the provider's message verbatim", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, _ := spawnFake(t, ctx, "fail", Config{})

		if err := r.Ready(ctx); err != nil {
			t.Fatalf("Ready() error = %v, want nil", err)
		}

		err := r.Deploy(ctx, &deploymentsv1.DeployRequest{Manifest: &deploymentsv1.Manifest{SchemaVersion: "provider.v1", Slug: "acme"}}, nil)

		var deployErr *DeployFailedError
		if !errors.As(err, &deployErr) {
			t.Fatalf("Deploy() error = %v (%T), want *DeployFailedError", err, err)
		}
		if deployErr.Message != "simulated deploy failure" {
			t.Errorf("DeployFailedError.Message = %q, want %q", deployErr.Message, "simulated deploy failure")
		}
		if err.Error() != "simulated deploy failure" {
			t.Errorf("DeployFailedError.Error() = %q, want the provider's message verbatim", err.Error())
		}
	})

	t.Run("calling before a successful Ready refuses rather than panicking", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, _ := spawnFake(t, ctx, "never-ready", Config{ReadyTimeout: 50 * time.Millisecond})

		err := r.Deploy(ctx, &deploymentsv1.DeployRequest{Manifest: &deploymentsv1.Manifest{SchemaVersion: "provider.v1", Slug: "acme"}}, nil)
		if !errors.Is(err, ErrDeploymentsUnavailable) {
			t.Fatalf("Deploy() error = %v, want ErrDeploymentsUnavailable", err)
		}
	})
}

func TestDeployFailedError(t *testing.T) {
	t.Parallel()

	t.Run("an empty message still reads as a failure", func(t *testing.T) {
		t.Parallel()

		err := (&DeployFailedError{}).Error()
		if strings.TrimSpace(err) == "" {
			t.Fatalf("DeployFailedError{}.Error() = %q, want a non-empty fallback", err)
		}
	})
}

func TestBootstrap(t *testing.T) {
	t.Parallel()

	t.Run("a successful bootstrap streams progress then a result", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, sockPath := spawnFake(t, ctx, "success", Config{})

		if err := r.Ready(ctx); err != nil {
			t.Fatalf("Ready() error = %v, want nil", err)
		}

		var events []*progressv1.OperationEvent
		err := r.Bootstrap(ctx, &deploymentsv1.BootstrapRequest{}, func(ev *progressv1.OperationEvent) { events = append(events, ev) })
		if err != nil {
			t.Fatalf("Bootstrap() error = %v, want nil", err)
		}
		if len(events) != 2 {
			t.Fatalf("got %d events, want 2 (progress, result)", len(events))
		}
		if result := events[1].GetResult(); result == nil || !result.GetSuccess() {
			t.Errorf("events[1] = %v, want a successful ResultEvent", events[1])
		}

		r.Close()
		assertProcessGone(t, r)
		assertNoStaleSocket(t, sockPath)
	})

	t.Run("a terminal failure carries the provider's message", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, _ := spawnFake(t, ctx, "fail", Config{})

		if err := r.Ready(ctx); err != nil {
			t.Fatalf("Ready() error = %v, want nil", err)
		}

		err := r.Bootstrap(ctx, &deploymentsv1.BootstrapRequest{}, nil)

		var failErr *DeployFailedError
		if !errors.As(err, &failErr) {
			t.Fatalf("Bootstrap() error = %v (%T), want *DeployFailedError", err, err)
		}
		if failErr.Message != "simulated bootstrap failure" {
			t.Errorf("DeployFailedError.Message = %q, want %q", failErr.Message, "simulated bootstrap failure")
		}
	})
}

func TestVars(t *testing.T) {
	t.Parallel()

	t.Run("reaching the store before a successful Ready refuses", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, _ := spawnFake(t, ctx, "never-ready", Config{ReadyTimeout: 50 * time.Millisecond})

		if _, err := r.Vars(); !errors.Is(err, ErrVarsUnavailable) {
			t.Fatalf("Vars() error = %v, want ErrVarsUnavailable", err)
		}
	})

	t.Run("a successful Ready hands back both clients", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, _ := spawnFake(t, ctx, "success", Config{})
		if err := r.Ready(ctx); err != nil {
			t.Fatalf("Ready() error = %v, want nil", err)
		}

		if vars, err := r.Vars(); err != nil || vars == nil {
			t.Errorf("Vars() = %v, %v, want a client and no error", vars, err)
		}
		if client, err := r.Deployments(); err != nil || client == nil {
			t.Errorf("Deployments() = %v, %v, want a client and no error", client, err)
		}
	})
}

func TestClose(t *testing.T) {
	t.Parallel()

	t.Run("cancelling the context reaps the provider without an orphan", func(t *testing.T) {
		t.Parallel()

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
	})

	t.Run("closing twice is a no-op the second time", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, _ := spawnFake(t, ctx, "success", Config{})
		if err := r.Ready(ctx); err != nil {
			t.Fatalf("Ready() error = %v, want nil", err)
		}

		r.Close()
		r.Close()
		assertProcessGone(t, r)
	})
}

func TestTeardownReapIsBounded(t *testing.T) {
	t.Parallel()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), fakeProviderEnvVar+"=1", fakeProviderModeEnvVar+"=never-ready")
	procgroup.New(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start process: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	r := &Runner{
		cmd:         cmd,
		gracePeriod: 50 * time.Millisecond,
		reapTimeout: 100 * time.Millisecond,
		done:        make(chan struct{}),
	}

	start := time.Now()
	r.teardown()
	elapsed := time.Since(start)

	const bound = 2 * time.Second
	if elapsed > bound {
		t.Fatalf("teardown() took %s, want it bounded well under %s when the process's output never reaches EOF", elapsed, bound)
	}
}

func assertProcessGone(t *testing.T, r *Runner) {
	t.Helper()
	select {
	case <-r.done:
	default:
		t.Error("provider process is still running after Close()")
	}
}

// Close removes the socket after teardown has reaped the process, so r.done
// closing does not imply the file is gone yet; poll rather than race it.
func assertNoStaleSocket(t *testing.T, sockPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := os.Stat(sockPath)
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("stale socket file left behind at %s (stat err = %v)", sockPath, err)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func init() {
	if _, err := os.Stat(os.Args[0]); err != nil {
		panic(fmt.Sprintf("providerrunner tests require os.Args[0] to be a runnable test binary: %v", err))
	}
}
