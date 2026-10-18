package node

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestEnsure(t *testing.T) {
	t.Parallel()

	t.Run("writes the whole bundled tree", func(t *testing.T) {
		t.Parallel()

		bundle := ensured(t, t.TempDir())
		for _, path := range []string{
			bundle.BuildScriptPath(),
			bundle.WorkerServerPath(),
			bundle.NextAdapterPath(),
			filepath.Join(bundle.dir, "next-adapter", "edge-node-entry.cjs"),
			filepath.Join(bundle.dir, "next-adapter", "next-dispatch.cjs"),
		} {
			info, err := os.Stat(path)
			if err != nil {
				t.Errorf("stat %s: %v", path, err)
				continue
			}
			if info.Size() == 0 {
				t.Errorf("%s is empty", path)
			}
		}
	})

	t.Run("lives in the cache under its stamp, never in a project", func(t *testing.T) {
		t.Parallel()

		cache := t.TempDir()
		bundle := ensured(t, cache)
		stamp, err := embedded.ReadFile("dist/STAMP")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(cache, string(bytes.TrimSpace(stamp))); bundle.dir != want {
			t.Errorf("bundle at %s, want %s", bundle.dir, want)
		}
	})

	t.Run("a bundle already installed is reused untouched", func(t *testing.T) {
		t.Parallel()

		cache := t.TempDir()
		bundle := ensured(t, cache)
		writeFile(t, bundle.BuildScriptPath(), "touched")

		again := ensured(t, cache)
		if got := readFile(t, again.BuildScriptPath()); got != "touched" {
			t.Error("Ensure rewrote a bundle already installed under its stamp")
		}
	})

	t.Run("runs installing at once all end with one whole tree", func(t *testing.T) {
		t.Parallel()

		cache := t.TempDir()
		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := range errs {
			wg.Go(func() { _, errs[i] = ensureIn(cache, time.Now()) })
		}
		wg.Wait()
		if err := errors.Join(errs...); err != nil {
			t.Fatalf("ensureIn: %v", err)
		}
		entries, err := os.ReadDir(cache)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("the cache holds %d entries, want the one bundle and no staging left behind", len(entries))
		}
		if info, err := os.Stat(ensured(t, cache).BuildScriptPath()); err != nil || info.Size() == 0 {
			t.Errorf("stat the build script = %v, want it whole", err)
		}
	})

	t.Run("a bundle unused for a month is removed when another is installed, and a recent one is kept", func(t *testing.T) {
		t.Parallel()

		cache := t.TempDir()
		now := time.Now()
		old, recent := filepath.Join(cache, "old-stamp"), filepath.Join(cache, "recent-stamp")
		for _, dir := range []string{old, recent} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(old, now.Add(-31*24*time.Hour), now.Add(-31*24*time.Hour)); err != nil {
			t.Fatal(err)
		}

		if _, err := ensureIn(cache, now); err != nil {
			t.Fatalf("ensureIn: %v", err)
		}
		if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat the month-old bundle = %v, want it removed", err)
		}
		if _, err := os.Stat(recent); err != nil {
			t.Errorf("stat the recent bundle = %v, want it kept: another CLI may still run from it", err)
		}
	})
}

func TestTheVariableEditorPageIsEmbedded(t *testing.T) {
	t.Parallel()

	t.Run("the index page loads the bundled script", func(t *testing.T) {
		t.Parallel()

		assets, err := VariableEditor()
		if err != nil {
			t.Fatalf("VariableEditor: %v", err)
		}
		page, err := fs.ReadFile(assets, "index.html")
		if err != nil {
			t.Fatalf("read the UI's index page: %v", err)
		}
		if !bytes.Contains(page, []byte("app.js")) {
			t.Errorf("index.html = %q, want it to load the bundled script", page)
		}
	})

	t.Run("the UI is served from the binary and never materialized on disk", func(t *testing.T) {
		t.Parallel()

		bundle := ensured(t, t.TempDir())
		materialized := filepath.Join(bundle.dir, variableEditorDir)
		if _, err := os.Stat(materialized); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat %s = %v, want it absent", materialized, err)
		}
	})
}

func ensured(t *testing.T, cache string) Bundle {
	t.Helper()
	bundle, err := ensureIn(cache, time.Now())
	if err != nil {
		t.Fatalf("ensureIn: %v", err)
	}
	return bundle
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
