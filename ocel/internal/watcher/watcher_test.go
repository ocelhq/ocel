package watcher

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatch_BurstOfChangesTriggersOnChangeOnce(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	if err := Watch(ctx, []string{dir}, 50*time.Millisecond, func() { calls.Add(1) }); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	for i := 0; i < 5; i++ {
		writeFile(t, filepath.Join(dir, "a.ts"), "export {};")
		time.Sleep(5 * time.Millisecond)
	}

	waitForCalls(t, &calls, 1)
	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want exactly 1 for a single debounced burst", got)
	}
}

func TestWatch_NewSubdirectoryIsWatchedForFutureChanges(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	if err := Watch(ctx, []string{dir}, 20*time.Millisecond, func() { calls.Add(1) }); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	waitForCalls(t, &calls, 1)

	writeFile(t, filepath.Join(sub, "new.ts"), "export {};")
	waitForCalls(t, &calls, 2)
}

func TestWatch_StopsReactingAfterContextCancelled(t *testing.T) {
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int32
	if err := Watch(ctx, []string{dir}, 10*time.Millisecond, func() { calls.Add(1) }); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	cancel()
	time.Sleep(30 * time.Millisecond)

	writeFile(t, filepath.Join(dir, "a.ts"), "export {};")
	time.Sleep(50 * time.Millisecond)

	if got := calls.Load(); got != 0 {
		t.Fatalf("calls after cancel = %d, want 0", got)
	}
}

func waitForCalls(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("calls = %d, never reached %d", calls.Load(), want)
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
