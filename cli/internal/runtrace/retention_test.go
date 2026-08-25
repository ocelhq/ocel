package runtrace

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneKeepsOnlyTheNewestRuns(t *testing.T) {
	dir := t.TempDir()
	const total = 14
	const keep = 10

	base := time.Now().Add(-time.Hour)
	for i := 0; i < total; i++ {
		stem := fmt.Sprintf("run%02d", i)
		mtime := base.Add(time.Duration(i) * time.Minute)
		for _, ext := range []string{".ndjson", ".otlp.json"} {
			p := filepath.Join(dir, stem+ext)
			if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
				t.Fatalf("write %s: %v", p, err)
			}
			if err := os.Chtimes(p, mtime, mtime); err != nil {
				t.Fatalf("chtimes %s: %v", p, err)
			}
		}
	}

	if err := Prune(dir, keep, time.Now()); err != nil {
		t.Fatalf("Prune() = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if got := len(entries); got != keep*2 {
		t.Fatalf("got %d files after prune, want %d (%d runs kept)", got, keep*2, keep)
	}

	for i := 0; i < total-keep; i++ {
		stem := fmt.Sprintf("run%02d", i)
		if _, err := os.Stat(filepath.Join(dir, stem+".ndjson")); !os.IsNotExist(err) {
			t.Errorf("oldest run %s should have been pruned", stem)
		}
	}
	for i := total - keep; i < total; i++ {
		stem := fmt.Sprintf("run%02d", i)
		if _, err := os.Stat(filepath.Join(dir, stem+".ndjson")); err != nil {
			t.Errorf("recent run %s should have survived prune: %v", stem, err)
		}
	}
}

func TestPruneIsANoOpUnderTheLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("run%d.ndjson", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if err := Prune(dir, RunRetention, time.Now()); err != nil {
		t.Fatalf("Prune() = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if got := len(entries); got != 3 {
		t.Errorf("got %d files, want all 3 kept", got)
	}
}

func TestPruneOnAMissingDirIsANoOp(t *testing.T) {
	if err := Prune(filepath.Join(t.TempDir(), "does-not-exist"), RunRetention, time.Now()); err != nil {
		t.Errorf("Prune() on a missing dir = %v, want nil", err)
	}
}

func TestPruneNeverRemovesAnEntryAsNewAsTheCutoff(t *testing.T) {
	dir := t.TempDir()
	const total = 12
	const keep = 10

	now := time.Now()
	cutoff := now.Add(-time.Hour)
	for i := 0; i < total; i++ {
		p := filepath.Join(dir, fmt.Sprintf("run%02d.ndjson", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		if err := os.Chtimes(p, now, now); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}

	if err := Prune(dir, keep, cutoff); err != nil {
		t.Fatalf("Prune() = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if got := len(entries); got != total {
		t.Errorf("got %d files after prune, want all %d protected because none are older than the cutoff", got, total)
	}
}

func TestPruneRemovesOnlyEntriesOlderThanTheCutoff(t *testing.T) {
	dir := t.TempDir()

	old := time.Now().Add(-time.Hour)
	for i := 0; i < 12; i++ {
		p := filepath.Join(dir, fmt.Sprintf("old%02d.ndjson", i))
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		if err := os.Chtimes(p, old.Add(time.Duration(i)*time.Minute), old.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("chtimes %s: %v", p, err)
		}
	}

	cutoff := time.Now()
	fresh := filepath.Join(dir, "fresh00.ndjson")
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", fresh, err)
	}
	if err := os.Chtimes(fresh, cutoff.Add(time.Minute), cutoff.Add(time.Minute)); err != nil {
		t.Fatalf("chtimes %s: %v", fresh, err)
	}

	if err := Prune(dir, 10, cutoff); err != nil {
		t.Fatalf("Prune() = %v", err)
	}

	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("entry newer than the cutoff should never be pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old00.ndjson")); !os.IsNotExist(err) {
		t.Errorf("oldest entry below the cutoff should have been pruned, err=%v", err)
	}
}
