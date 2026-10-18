package workdir

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTwoRunsWriteTheSameNameToDifferentFiles(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	first, second := NewRun(), NewRun()
	a, err := first.Write(base, "entry.mjs", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Write(base, "entry.mjs", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both runs wrote %s, want a path each", a)
	}
	if got, _ := os.ReadFile(a); string(got) != "first" {
		t.Errorf("the first run's file holds %q, want what it wrote", got)
	}
}

func TestRemovingARunRemovesWhatItWroteAndNothingElse(t *testing.T) {
	t.Parallel()

	base, other := t.TempDir(), t.TempDir()
	run, kept := NewRun(), NewRun()
	written, err := run.Write(base, "entry.mjs", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run.Write(other, "worker.mjs", []byte("x")); err != nil {
		t.Fatal(err)
	}
	survivor, err := kept.Write(base, "entry.mjs", []byte("y"))
	if err != nil {
		t.Fatal(err)
	}

	if err := run.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, dir := range []string{filepath.Dir(written), run.Path(other)} {
		if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat %s = %v, want it removed", dir, err)
		}
	}
	if _, err := os.Stat(survivor); err != nil {
		t.Errorf("another run's file is gone: %v", err)
	}
}

func TestASharedFileIsNamedForItsContentAndWrittenWhole(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var wg sync.WaitGroup
	paths := make([]string, 16)
	errs := make([]error, 16)
	for i := range paths {
		wg.Go(func() { paths[i], errs[i] = WriteShared(dir, "main.go", []byte("package main\n")) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if path != paths[0] {
			t.Fatalf("one content was written to %s and %s, want one path", paths[0], path)
		}
	}
	if got, _ := os.ReadFile(paths[0]); string(got) != "package main\n" {
		t.Errorf("the shared file holds %q", got)
	}
	entries, err := os.ReadDir(filepath.Dir(paths[0]))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the shared directory holds %d entries, want the file and no staged copy", len(entries))
	}

	other, err := WriteShared(dir, "main.go", []byte("package main\n\nfunc init() {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if other == paths[0] {
		t.Error("two contents share one path, want each its own")
	}
}

func TestTheBuildOutputIsHeldByOneRunAtATime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	release, err := LockBuildOutput(context.Background(), dir, func() { t.Error("the first run waited on a free lock") })
	if err != nil {
		t.Fatal(err)
	}

	var waited atomic.Bool
	acquired := make(chan func(), 1)
	go func() {
		second, err := LockBuildOutput(context.Background(), dir, func() { waited.Store(true) })
		if err != nil {
			t.Error(err)
			return
		}
		acquired <- second
	}()
	select {
	case <-acquired:
		t.Fatal("a second run took the lock while the first held it")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case second := <-acquired:
		second()
	case <-time.After(5 * time.Second):
		t.Fatal("the second run never took the lock once it was released")
	}
	if !waited.Load() {
		t.Error("the second run was never told it was waiting")
	}
}

func TestWaitingForTheBuildOutputEndsWithItsContext(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	release, err := LockBuildOutput(context.Background(), dir, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := LockBuildOutput(ctx, dir, func() {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Lock = %v, want the wait to end with its context", err)
	}
}
