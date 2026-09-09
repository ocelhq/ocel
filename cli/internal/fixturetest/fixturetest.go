package fixturetest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func RepoDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate the test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}

func Dirs(t *testing.T) []string {
	t.Helper()
	var dirs []string
	fixtures := filepath.Join(RepoDir(t), "tests", "fixtures")
	concerns, err := os.ReadDir(fixtures)
	if err != nil {
		t.Fatalf("read the fixtures: %v", err)
	}
	for _, concern := range concerns {
		if !concern.IsDir() {
			continue
		}
		named, err := os.ReadDir(filepath.Join(fixtures, concern.Name()))
		if err != nil {
			t.Fatalf("read the %s fixtures: %v", concern.Name(), err)
		}
		for _, fixture := range named {
			if fixture.IsDir() {
				dirs = append(dirs, filepath.Join(fixtures, concern.Name(), fixture.Name()))
			}
		}
	}
	return dirs
}

func ConfigsIn(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && projectconfig.IsConfig(entry.Name()) {
			found = append(found, filepath.Join(dir, entry.Name()))
		}
	}
	return found
}

func IsNode(t *testing.T, dir string) bool {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
		return true
	}
	for _, path := range ConfigsIn(t, dir) {
		if projectconfig.IsProgram(path) {
			return true
		}
	}
	return false
}
