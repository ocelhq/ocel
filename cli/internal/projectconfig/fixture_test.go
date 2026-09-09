package projectconfig

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func repoDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate the test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

func TestTheGoFixtureDeploysFromJSONAlone(t *testing.T) {
	dir := filepath.Join(repoDir(t), "tests", "fixtures", "deploy", "go")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("the go fixture is not checked out: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the fixture: %v", err)
	}
	for _, entry := range entries {
		if _, form, ok := formOf(entry.Name()); ok && form.suffix == ".config.ts" {
			t.Fatalf("the go fixture still carries %s", entry.Name())
		}
	}

	t.Setenv("PATH", "")
	cfg, err := Resolve(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("resolve the go fixture with no node on PATH: %v", err)
	}
	if cfg.Path != filepath.Join(dir, DefaultFileName) {
		t.Fatalf("path = %q", cfg.Path)
	}
	if cfg.Provider == nil || cfg.Provider.Name != "aws" {
		t.Fatalf("provider = %+v", cfg.Provider)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Runtime.Name != "go" {
		t.Fatalf("apps = %+v", cfg.Apps)
	}
}
