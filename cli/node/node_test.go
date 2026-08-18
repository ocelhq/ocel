package node

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsure(t *testing.T) {
	t.Parallel()

	t.Run("writes the whole bundled tree", func(t *testing.T) {
		t.Parallel()

		dir := ensured(t)
		for _, path := range []string{
			BuilderPath(dir),
			AdapterPath(dir),
			filepath.Join(filepath.Dir(AdapterPath(dir)), "edge-cache-handler.cjs"),
			filepath.Join(filepath.Dir(AdapterPath(dir)), "edge-node-entry.cjs"),
			filepath.Join(filepath.Dir(AdapterPath(dir)), "next-dispatch.cjs"),
			WorkerBundles(dir)["cloudflare"],
			StoreWorkerBundles(dir)["cloudflare"],
			stampPath(dir),
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

	t.Run("a matching stamp leaves the tree untouched", func(t *testing.T) {
		t.Parallel()

		dir := ensured(t)
		writeFile(t, BuilderPath(dir), "touched")

		if err := Ensure(dir); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if got := readFile(t, BuilderPath(dir)); got != "touched" {
			t.Error("Ensure rewrote the tree despite a matching STAMP")
		}
	})

	t.Run("a mismatched stamp rewrites the tree and clears what it does not own", func(t *testing.T) {
		t.Parallel()

		dir := ensured(t)
		want := readFile(t, stampPath(dir))
		writeFile(t, stampPath(dir), "stale")

		stale := filepath.Join(DistDir(dir), "leftover.txt")
		writeFile(t, stale, "x")

		if err := Ensure(dir); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if got := readFile(t, stampPath(dir)); got != want {
			t.Error("STAMP not restored")
		}
		if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
			t.Error("stale file survived the rewrite")
		}
	})

	t.Run("a run interrupted before the stamp was written is redone", func(t *testing.T) {
		t.Parallel()

		dir := ensured(t)
		if err := os.Remove(stampPath(dir)); err != nil {
			t.Fatal(err)
		}
		writeFile(t, BuilderPath(dir), "partial")

		if err := Ensure(dir); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if got := readFile(t, BuilderPath(dir)); got == "partial" {
			t.Error("Ensure left the partial builder in place")
		}
		if _, err := os.Stat(stampPath(dir)); err != nil {
			t.Errorf("STAMP not written: %v", err)
		}
	})
}

func TestVarsUI(t *testing.T) {
	t.Parallel()

	t.Run("the index page loads the bundled script", func(t *testing.T) {
		t.Parallel()

		assets, err := VarsUI()
		if err != nil {
			t.Fatalf("VarsUI: %v", err)
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

		dir := ensured(t)
		materialized := filepath.Join(DistDir(dir), varsUIDir)
		if _, err := os.Stat(materialized); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat %s = %v, want it absent", materialized, err)
		}
	})
}

func ensured(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := Ensure(dir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	return dir
}

func stampPath(dir string) string { return filepath.Join(DistDir(dir), "STAMP") }

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
