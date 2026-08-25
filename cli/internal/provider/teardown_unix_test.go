//go:build unix

package provider

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

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
		out := &syncBuffer{}
		r, pidFile := spawnOrphan(t, ctx, "orphan-holds-pipe", Config{
			Stdout:      out,
			GracePeriod: 100 * time.Millisecond,
			ReapTimeout: 200 * time.Millisecond,
		})

		grandchildPid := readGrandchildPid(t, pidFile)
		t.Cleanup(func() { _ = syscall.Kill(grandchildPid, syscall.SIGKILL) })

		start := time.Now()
		r.Close()
		elapsed := time.Since(start)

		const bound = 3 * time.Second
		if elapsed > bound {
			t.Fatalf("Close() took %s, want it bounded well under %s even with a grandchild holding the pipe", elapsed, bound)
		}

		if !processAlive(grandchildPid) {
			t.Fatalf("grandchild %d died with the group, so Close() never reached the reap timeout this test exists to cover", grandchildPid)
		}
		select {
		case <-r.done:
			t.Fatal("the provider was reaped, so Close() returned on <-done rather than the reap timeout")
		default:
		}

		written := out.Len()
		time.Sleep(300 * time.Millisecond)
		if grew := out.Len() - written; grew != 0 {
			t.Fatalf("stdout grew by %d bytes after Close() returned, want the drains muted once teardown gives up on the reap", grew)
		}
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

		if err := syscall.Kill(r.cmd.Process.Pid, syscall.SIGTERM); err != nil {
			t.Fatalf("kill leader: %v", err)
		}

		select {
		case <-r.done:
		case <-time.After(2 * time.Second):
			t.Fatal("provider was never reaped")
		}
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
