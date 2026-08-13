//go:build unix

package providerrunner

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func spawnOrphan(t *testing.T, ctx context.Context, mode string, cfg Config) (*Runner, string) {
	t.Helper()

	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	cfg.Env = append([]string{fakeProviderGrandchildPidFileEnvVar + "=" + pidFile}, cfg.Env...)

	r, _ := spawnFake(t, ctx, mode, cfg)
	return r, pidFile
}

func readGrandchildPid(t *testing.T, pidFile string) int {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(pidFile)
		if err == nil {
			pid, convErr := strconv.Atoi(string(data))
			if convErr != nil {
				t.Fatalf("grandchild pidfile %s contains %q, want an integer", pidFile, data)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild never wrote its pid to %s: %v", pidFile, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func assertProcessDead(t *testing.T, pid int) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for processAlive(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("pid %d is still alive after teardown", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTeardownBound(t *testing.T) {
	t.Parallel()

	t.Run("a grandchild holding the output pipe cannot make teardown hang", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, pidFile := spawnOrphan(t, ctx, "orphan-holds-pipe", Config{
			GracePeriod: 100 * time.Millisecond,
			ReapTimeout: 200 * time.Millisecond,
		})

		grandchildPid := readGrandchildPid(t, pidFile)

		start := time.Now()
		r.Close()
		elapsed := time.Since(start)

		const bound = 3 * time.Second
		if elapsed > bound {
			t.Fatalf("Close() took %s, want it bounded well under %s even with a grandchild holding the pipe", elapsed, bound)
		}

		assertProcessDead(t, grandchildPid)
	})

	t.Run("the process group is force-killed even when the provider exits inside the grace window", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, pidFile := spawnOrphan(t, ctx, "orphan-detached-pipe", Config{
			GracePeriod: 2 * time.Second,
			ReapTimeout: 200 * time.Millisecond,
		})

		grandchildPid := readGrandchildPid(t, pidFile)

		start := time.Now()
		r.Close()
		elapsed := time.Since(start)

		const bound = 1 * time.Second
		if elapsed > bound {
			t.Fatalf("Close() took %s, want it to return well before the 2s grace period once the provider exits on its own", elapsed)
		}

		assertProcessDead(t, grandchildPid)
	})

	t.Run("a grandchild is swept at reap time, not left for a Close that arrives long after", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		r, pidFile := spawnOrphan(t, ctx, "orphan-detached-pipe", Config{
			GracePeriod: 2 * time.Second,
			ReapTimeout: 200 * time.Millisecond,
		})

		grandchildPid := readGrandchildPid(t, pidFile)

		// Kill only the leader, the way a real provider exits on its own
		// once it decides it is done, without ever calling Close(). If the
		// grandchild only got swept from teardown() (i.e. from Close()),
		// it would still be alive at this point.
		if err := syscall.Kill(r.cmd.Process.Pid, syscall.SIGTERM); err != nil {
			t.Fatalf("kill leader: %v", err)
		}

		select {
		case <-r.done:
		case <-time.After(2 * time.Second):
			t.Fatal("provider was never reaped")
		}
		// The waiter goroutine sweeps the group in the same breath it
		// reaps the leader, so the grandchild must already be dead here —
		// teardown() (which only runs from Close()) never got involved.
		// This is what makes it safe for Close() to be deferred to the end
		// of a session that can run for minutes after the provider itself
		// exited: by then the group has long since been swept at the one
		// instant its pgid was guaranteed not to have been recycled.
		assertProcessDead(t, grandchildPid)

		start := time.Now()
		r.Close()
		elapsed := time.Since(start)

		const bound = 500 * time.Millisecond
		if elapsed > bound {
			t.Fatalf("Close() took %s after the provider was already reaped, want it near-instant", elapsed)
		}
	})
}
