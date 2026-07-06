package projectconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindConfigFile_WalksUpFromNestedSubdirectory(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, ConfigFileName)
	if err := os.WriteFile(configPath, []byte("export default {};"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	found, err := findConfigFile(nested)
	if err != nil {
		t.Fatalf("findConfigFile: %v", err)
	}
	if found != configPath {
		t.Fatalf("found = %q, want %q", found, configPath)
	}
}

func TestFindConfigFile_NotFound(t *testing.T) {
	root := t.TempDir()

	_, err := findConfigFile(root)
	if !os.IsNotExist(err) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func writeConfig(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, ConfigFileName)
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestResolve_ValidConfig(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
  discovery: { paths: ["resources"] },
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.ProjectID != "proj_123" {
		t.Fatalf("ProjectID = %q, want %q", cfg.ProjectID, "proj_123")
	}
	if len(cfg.Discovery.Paths) != 1 || cfg.Discovery.Paths[0] != "resources" {
		t.Fatalf("Discovery.Paths = %v, want [resources]", cfg.Discovery.Paths)
	}
}

func TestResolve_DefaultsDiscoveryPaths(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_456",
};
`)

	cfg, err := Resolve(root)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(cfg.Discovery.Paths) != 1 || cfg.Discovery.Paths[0] != "ocel" {
		t.Fatalf("Discovery.Paths = %v, want [ocel]", cfg.Discovery.Paths)
	}
}

func TestResolve_MissingConfig(t *testing.T) {
	root := t.TempDir()

	_, err := Resolve(root)
	if err == nil {
		t.Fatal("Resolve: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %q, want it to mention `ocel init`", err.Error())
	}
}

func TestResolve_UnparseableConfig(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_789"
  this is not valid typescript +++
`)

	_, err := Resolve(root)
	if err == nil {
		t.Fatal("Resolve: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %q, want it to mention `ocel init`", err.Error())
	}
}

func TestResolve_MissingProjectID(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  discovery: { paths: ["x"] },
};
`)

	_, err := Resolve(root)
	if err == nil {
		t.Fatal("Resolve: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "ocel init") {
		t.Fatalf("err = %q, want it to mention `ocel init`", err.Error())
	}
}

func TestResolve_WritesBuildArtifactsUnderOcelDir(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, `
export default {
  projectId: "proj_123",
};
`)

	if _, err := Resolve(root); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	artifact := filepath.Join(root, buildDirName, "config.mjs")
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("expected build artifact at %s: %v", artifact, err)
	}
}
