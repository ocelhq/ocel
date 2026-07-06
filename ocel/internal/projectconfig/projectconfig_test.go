package projectconfig

import (
	"os"
	"path/filepath"
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
