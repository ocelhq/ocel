package runtrace

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func writeRunDirectory(t *testing.T, runs, name string, modified time.Time) string {
	t.Helper()
	dir := filepath.Join(runs, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{EventsFileName, TraceFileName} {
		writeModified(t, filepath.Join(dir, file), modified)
	}
	if err := os.Chtimes(dir, modified, modified); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeModified(t *testing.T, path string, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func entriesOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestPruneKeepsTheNewestRunDirectoriesAndRemovesTheRestWhole(t *testing.T) {
	runs := t.TempDir()
	base := time.Now().Add(-time.Hour)
	var dirs []string
	for i := range 14 {
		dirs = append(dirs, writeRunDirectory(t, runs, fmt.Sprintf("run%02d", i), base.Add(time.Duration(i)*time.Minute)))
	}

	if err := prune(runs, 10, time.Now()); err != nil {
		t.Fatalf("prune() = %v", err)
	}

	for i, dir := range dirs {
		_, err := os.Stat(dir)
		switch {
		case i < 4 && !os.IsNotExist(err):
			t.Errorf("old run %s was kept, want it removed with everything in it", filepath.Base(dir))
		case i >= 4 && err != nil:
			t.Errorf("recent run %s was removed: %v", filepath.Base(dir), err)
		}
	}
}

func TestPruneDatesARunByTheNewestFileItHolds(t *testing.T) {
	runs := t.TempDir()
	old := time.Now().Add(-time.Hour)
	stillWriting := writeRunDirectory(t, runs, "run00", old)
	writeModified(t, filepath.Join(stillWriting, EventsFileName), old.Add(time.Hour-time.Second))
	for i := 1; i <= 10; i++ {
		writeRunDirectory(t, runs, fmt.Sprintf("run%02d", i), old.Add(time.Duration(i)*time.Minute))
	}

	if err := prune(runs, 10, time.Now()); err != nil {
		t.Fatalf("prune() = %v", err)
	}

	if _, err := os.Stat(stillWriting); err != nil {
		t.Errorf("a run whose events file was written last was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runs, "run01")); !os.IsNotExist(err) {
		t.Errorf("the run written least recently was kept, err=%v", err)
	}
}

func TestPruneRemovesTheFlatFilesReleasedVersionsLeftInTheRunsDirectory(t *testing.T) {
	runs := t.TempDir()
	old := time.Now().Add(-time.Hour)
	const id = "4bf92f3577b34da6a3ce929d0e0e4736"
	for _, legacy := range []string{id + ".ndjson", id + ".otlp.json", id + ".otlp.json.partial"} {
		writeModified(t, filepath.Join(runs, legacy), old)
	}
	writeModified(t, filepath.Join(runs, "notes.txt"), old)
	writeRunDirectory(t, runs, "run00", old)

	if err := prune(runs, 10, time.Now()); err != nil {
		t.Fatalf("prune() = %v", err)
	}

	if got := entriesOf(t, runs); !slices.Equal(got, []string{"notes.txt", "run00"}) {
		t.Errorf("the runs directory holds %q, want the run directory and the file no version of ocel wrote", got)
	}
}

func TestPruneOnAMissingDirIsANoOp(t *testing.T) {
	if err := prune(filepath.Join(t.TempDir(), "does-not-exist"), retainedRuns, time.Now()); err != nil {
		t.Errorf("prune() on a missing dir = %v, want nil", err)
	}
}

func TestPruneNeverRemovesARunWrittenSinceTheCutoff(t *testing.T) {
	runs := t.TempDir()
	cutoff := time.Now().Add(-time.Hour)
	for i := range 12 {
		writeRunDirectory(t, runs, fmt.Sprintf("run%02d", i), time.Now())
	}
	writeModified(t, filepath.Join(runs, "4bf92f3577b34da6a3ce929d0e0e4736.ndjson"), time.Now())

	if err := prune(runs, 10, cutoff); err != nil {
		t.Fatalf("prune() = %v", err)
	}

	if got := len(entriesOf(t, runs)); got != 13 {
		t.Errorf("the runs directory holds %d entries after prune, want all 13: a run written since the cutoff may still be running", got)
	}
}
