package obs

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

	if err := Prune(dir, keep); err != nil {
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
	if err := Prune(dir, RunRetention); err != nil {
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
	if err := Prune(filepath.Join(t.TempDir(), "does-not-exist"), RunRetention); err != nil {
		t.Errorf("Prune() on a missing dir = %v, want nil", err)
	}
}
