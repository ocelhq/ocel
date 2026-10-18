package filewatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
		start(t, clock, Paths{Dirs: []string{dir}})

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
		w := start(t, clock, Paths{Dirs: []string{dir}})

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
		if paths := w.WatchList(); !slices.Contains(paths, sub) {
			t.Fatalf("watching %v, want %s among them", paths, sub)
		}
	})

	t.Run("every path beside the watched file is ignored", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		dotfile := filepath.Join(dir, ".env")
		clock := newFakeClock()
		start(t, clock, Paths{Files: []string{dotfile}})

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
		w := start(t, clock, Paths{Files: []string{dotfile}})

		sub := filepath.Join(root, "node_modules")
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		writeFile(t, dotfile, "API_TOKEN=first")
		clock.waitArmed(t, 1)

		if paths := w.WatchList(); slices.Contains(paths, sub) {
			t.Fatalf("watching %s, want only the file's own directory", sub)
		}
	})

	t.Run("a cancelled context stops the watch goroutine", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		clock := newFakeClock()

		w, err := Start(ctx, Config{
			Paths:    Paths{Dirs: []string{dir}},
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

	t.Run("a tree moved in is watched down to its deepest directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		clock := newFakeClock()
		w := start(t, clock, Paths{Dirs: []string{dir}})

		outside := filepath.Join(t.TempDir(), "pkg")
		if err := os.MkdirAll(filepath.Join(outside, "nested", "deeper"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(outside, filepath.Join(dir, "pkg")); err != nil {
			t.Fatal(err)
		}
		clock.waitArmed(t, 1)
		clock.elapse(t)

		deeper := filepath.Join(dir, "pkg", "nested", "deeper")
		if paths := w.WatchList(); !slices.Contains(paths, deeper) {
			t.Fatalf("watching %v, want %s among them", paths, deeper)
		}
		writeFile(t, filepath.Join(deeper, "a.ts"), "export {};")
		clock.waitArmed(t, 2)
	})

	t.Run("a skipped directory is never watched and never a change", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		clock := newFakeClock()
		w := startWith(t, Config{
			Paths:    Paths{Dirs: []string{dir}},
			SkipDir:  func(name string) bool { return name == "node_modules" },
			OnChange: clock.onChange,
			newTimer: clock.newTimer,
		})

		modules := filepath.Join(dir, "node_modules")
		if err := os.MkdirAll(filepath.Join(modules, "dep"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(modules, "dep", "index.js"), "")
		time.Sleep(200 * time.Millisecond)

		if got := clock.arms(); got != 0 {
			t.Errorf("debounce armed %d times by a skipped directory, want 0", got)
		}
		if paths := w.WatchList(); slices.ContainsFunc(paths, func(p string) bool { return strings.HasPrefix(p, modules) }) {
			t.Errorf("watching %v, want nothing under %s", paths, modules)
		}

		writeFile(t, filepath.Join(dir, "a.ts"), "export {};")
		clock.waitArmed(t, 1)
	})

	t.Run("changes keep being seen while a change is being handled", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		clock := newFakeClock()
		handling, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		startWith(t, Config{
			Paths: Paths{Dirs: []string{dir}},
			OnChange: func() {
				once.Do(func() { close(handling) })
				<-release
			},
			newTimer: clock.newTimer,
		})
		defer close(release)

		writeFile(t, filepath.Join(dir, "a.ts"), "export {};")
		clock.waitArmed(t, 1)
		time.Sleep(100 * time.Millisecond)
		armed := clock.arms()
		clock.tick <- time.Now()
		<-handling

		writeFile(t, filepath.Join(dir, "b.ts"), "export {};")
		clock.waitArmed(t, armed+1)
	})

	t.Run("a directory moved away stops being watched", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		sub := filepath.Join(dir, "sub")
		if err := os.MkdirAll(filepath.Join(sub, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		clock := newFakeClock()
		w := start(t, clock, Paths{Dirs: []string{dir, sub, filepath.Join(sub, "nested")}})

		if err := os.Rename(sub, filepath.Join(t.TempDir(), "gone")); err != nil {
			t.Fatal(err)
		}
		clock.waitArmed(t, 1)
		clock.elapse(t)

		if paths := w.WatchList(); slices.ContainsFunc(paths, func(p string) bool { return strings.HasPrefix(p, sub) }) {
			t.Errorf("watching %v, want nothing under %s once it moved away", paths, sub)
		}
	})
}

func start(t *testing.T, clock *fakeClock, paths Paths) *Watcher {
	t.Helper()
	return startWith(t, Config{Paths: paths, OnChange: clock.onChange, newTimer: clock.newTimer})
}

func startWith(t *testing.T, cfg Config) *Watcher {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	w, err := Start(ctx, cfg)
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
