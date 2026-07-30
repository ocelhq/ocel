package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureWritesTree(t *testing.T) {
	dir := t.TempDir()
	if err := Ensure(dir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	for _, path := range []string{
		BuilderPath(dir),
		AdapterPath(dir),
		filepath.Join(filepath.Dir(AdapterPath(dir)), "edge-cache-handler.cjs"),
		WorkerBundles(dir)["next"]["cloudflare"],
		StoreWorkerBundles(dir)["cloudflare"],
		filepath.Join(dir, ".ocel", "dist", "STAMP"),
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
}

func TestEnsureNoOpsOnStampMatch(t *testing.T) {
	dir := t.TempDir()
	if err := Ensure(dir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := os.WriteFile(BuilderPath(dir), []byte("touched"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(dir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	got, err := os.ReadFile(BuilderPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "touched" {
		t.Error("Ensure rewrote the tree despite a matching STAMP")
	}
}

func TestEnsureRewritesOnStampMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := Ensure(dir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	stampPath := filepath.Join(dir, ".ocel", "dist", "STAMP")
	want, err := os.ReadFile(stampPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stampPath, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, ".ocel", "dist", "leftover.txt")
	if err := os.WriteFile(stale, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(dir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	got, err := os.ReadFile(stampPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Error("STAMP not restored")
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("stale file survived the rewrite")
	}
}

func TestEnsureRedoesInterruptedRun(t *testing.T) {
	dir := t.TempDir()
	if err := Ensure(dir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	stampPath := filepath.Join(dir, ".ocel", "dist", "STAMP")
	if err := os.Remove(stampPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BuilderPath(dir), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Ensure(dir); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	got, err := os.ReadFile(BuilderPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == "partial" {
		t.Error("Ensure left the partial builder in place")
	}
	if _, err := os.Stat(stampPath); err != nil {
		t.Errorf("STAMP not written: %v", err)
	}
}
