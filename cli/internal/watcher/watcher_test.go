package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatch(t *testing.T) {
	t.Parallel()

	t.Run("a burst of changes arms the debounce once and fires once", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		clock := newFakeClock()
		start(t, clock, Set{Dirs: []string{dir}})

		const writes = 5
		for i := range writes {
			writeFile(t, filepath.Join(dir, fmt.Sprintf("a%d.ts", i)), "export {};")
		}
		clock.waitArmed(t, writes)

		if got := clock.calls(); got != 0 {
			t.Fatalf("calls = %d while the debounce is still pending, want 0", got)
		}

		clock.elapse(t)
		if got := clock.calls(); got != 1 {
			t.Fatalf("calls = %d, want exactly 1 for a single debounced burst", got)
		}
	})

	t.Run("a new subdirectory is watched for future changes", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		clock := newFakeClock()
		w := start(t, clock, Set{Dirs: []string{dir}})

		sub := filepath.Join(dir, "sub")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		clock.waitArmed(t, 1)
		clock.elapse(t)

		writeFile(t, filepath.Join(sub, "new.ts"), "export {};")
		clock.waitArmed(t, 2)
		clock.elapse(t)

		if got := clock.calls(); got != 2 {
			t.Fatalf("calls = %d, want 2 (the directory, then the file inside it)", got)
		}
		if paths := w.Paths(); !slices.Contains(paths, sub) {
			t.Fatalf("watching %v, want %s among them", paths, sub)
		}
	})

	t.Run("every path beside the watched file is ignored", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		dotfile := filepath.Join(dir, ".env")
		clock := newFakeClock()
		start(t, clock, Set{Files: []string{dotfile}})

		writeFile(t, filepath.Join(dir, "package.json"), "{}")
		time.Sleep(200 * time.Millisecond)
		if got := clock.arms(); got != 0 {
			t.Fatalf("debounce armed %d times by a rejected path, want 0", got)
		}

		writeFile(t, dotfile, "API_TOKEN=first")
		clock.waitArmed(t, 1)
		clock.elapse(t)
		if got := clock.calls(); got != 1 {
			t.Fatalf("calls = %d, want 1", got)
		}
	})

	t.Run("a files directory does not recurse into new subdirectories", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		dotfile := filepath.Join(root, ".env")
		clock := newFakeClock()
		w := start(t, clock, Set{Files: []string{dotfile}})

		sub := filepath.Join(root, "node_modules")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		writeFile(t, dotfile, "API_TOKEN=first")
		clock.waitArmed(t, 1)

		if paths := w.Paths(); slices.Contains(paths, sub) {
			t.Fatalf("watching %s, want only the file's own directory", sub)
		}
	})

	t.Run("a cancelled context stops the watch goroutine", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		clock := newFakeClock()

		w, err := Start(ctx, Config{
			Set:      Set{Dirs: []string{dir}},
			OnChange: clock.onChange,
			newTimer: clock.newTimer,
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}

		cancel()
		waitStopped(t, w)

		writeFile(t, filepath.Join(dir, "a.ts"), "export {};")
		time.Sleep(50 * time.Millisecond)

		if got := clock.calls(); got != 0 {
			t.Fatalf("calls after cancel = %d, want 0", got)
		}
		if got := clock.arms(); got != 0 {
			t.Fatalf("debounce armed %d times after cancel, want 0", got)
		}
	})
}

func start(t *testing.T, clock *fakeClock, set Set) *Watcher {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	w, err := Start(ctx, Config{Set: set, OnChange: clock.onChange, newTimer: clock.newTimer})
	if err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		waitStopped(t, w)
	})
	return w
}

func waitStopped(t *testing.T, w *Watcher) {
	t.Helper()
	select {
	case <-w.Done():
	case <-time.After(10 * time.Second):
		t.Error("the watch goroutine never stopped after its context was cancelled")
	}
}

type fakeClock struct {
	mu    sync.Mutex
	armed bool

	tick  chan time.Time
	fired chan struct{}

	resets atomic.Int32
	fires  atomic.Int32
}

func newFakeClock() *fakeClock {
	return &fakeClock{armed: true, tick: make(chan time.Time), fired: make(chan struct{})}
}

func (c *fakeClock) newTimer(time.Duration) timer { return c }

func (c *fakeClock) C() <-chan time.Time { return c.tick }

func (c *fakeClock) Stop() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	was := c.armed
	c.armed = false
	return was
}

func (c *fakeClock) Reset(time.Duration) bool {
	c.mu.Lock()
	was := c.armed
	c.armed = true
	c.mu.Unlock()
	c.resets.Add(1)
	return was
}

func (c *fakeClock) onChange() {
	c.fires.Add(1)
	c.fired <- struct{}{}
}

func (c *fakeClock) arms() int32 { return c.resets.Load() }

func (c *fakeClock) calls() int32 { return c.fires.Load() }

func (c *fakeClock) waitArmed(t *testing.T, want int32) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if c.resets.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("debounce armed %d times, never reached %d", c.resets.Load(), want)
}

func (c *fakeClock) elapse(t *testing.T) {
	t.Helper()
	select {
	case c.tick <- time.Now():
	case <-time.After(10 * time.Second):
		t.Fatal("the watch goroutine never took the elapsed debounce")
	}
	select {
	case <-c.fired:
	case <-time.After(10 * time.Second):
		t.Fatal("the elapsed debounce never reached the change callback")
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
