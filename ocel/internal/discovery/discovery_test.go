package discovery

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDiscover_FindsFilesUnderDefaultPath(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ocel", "main.ts"), "export {};")
	write(t, filepath.Join(root, "ocel", "sub", "nested.ts"), "export {};")
	write(t, filepath.Join(root, "other.ts"), "export {};")

	got, err := Discover(root, []string{"ocel"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	want := []string{
		filepath.Join(root, "ocel", "main.ts"),
		filepath.Join(root, "ocel", "sub", "nested.ts"),
	}
	assertFiles(t, got, want)
}

func TestDiscover_IgnoresNodeModulesAndHiddenDirs(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ocel", "main.ts"), "export {};")
	write(t, filepath.Join(root, "ocel", "node_modules", "dep.ts"), "export {};")
	write(t, filepath.Join(root, "ocel", ".hidden", "skip.ts"), "export {};")

	got, err := Discover(root, []string{"ocel"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	want := []string{filepath.Join(root, "ocel", "main.ts")}
	assertFiles(t, got, want)
}

func TestDiscover_FiltersNonSourceExtensions(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ocel", "main.ts"), "export {};")
	write(t, filepath.Join(root, "ocel", "README.md"), "# not source")

	got, err := Discover(root, []string{"ocel"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	want := []string{filepath.Join(root, "ocel", "main.ts")}
	assertFiles(t, got, want)
}

func TestDiscover_SupportsGlobPatternsAcrossPackages(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "packages", "a", "ocel", "one.ts"), "export {};")
	write(t, filepath.Join(root, "packages", "b", "ocel", "two.ts"), "export {};")

	got, err := Discover(root, []string{"packages/*/ocel"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	want := []string{
		filepath.Join(root, "packages", "a", "ocel", "one.ts"),
		filepath.Join(root, "packages", "b", "ocel", "two.ts"),
	}
	assertFiles(t, got, want)
}

func TestDiscover_MissingPathYieldsNoFilesNoError(t *testing.T) {
	root := t.TempDir()

	got, err := Discover(root, []string{"ocel"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Discover() = %v, want empty", got)
	}
}

func assertFiles(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Discover() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Discover() = %v, want %v", got, want)
		}
	}
}
