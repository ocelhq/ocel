package build

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/project"
)

func TestHasJS(t *testing.T) {
	t.Run("a declaration root written in JS contains JS", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, discovery.DefaultRootDirName)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.ts"), []byte("export {};"), 0o644); err != nil {
			t.Fatal(err)
		}

		hasJS, err := HasJS(&project.Project{Dir: root})
		if err != nil {
			t.Fatalf("HasJS: %v", err)
		}
		if !hasJS {
			t.Error("HasJS = false, want the ts declaration root read as JS")
		}
	})

	t.Run("a discovery path that is not there is an error, not a JS project", func(t *testing.T) {
		root := t.TempDir()
		cfg := &project.Project{Dir: root}
		cfg.DiscoveryPaths = []string{"nowhere"}

		hasJS, err := HasJS(cfg)
		if err == nil {
			t.Fatalf("HasJS = %v, nil error, want the unreadable roots reported", hasJS)
		}
		if hasJS {
			t.Error("HasJS = true for roots it could not read")
		}
	})
}

func TestTheFixturesOfAnotherLanguageHaveNoJS(t *testing.T) {
	tried := 0
	for _, dir := range fixturetest.Dirs(t) {
		if fixturetest.IsNode(t, dir) {
			continue
		}
		tried++
		t.Run(filepath.Base(filepath.Dir(dir))+"/"+filepath.Base(dir), func(t *testing.T) {
			hasJS, err := HasJS(&project.Project{Dir: dir})
			if err != nil {
				t.Fatalf("HasJS: %v", err)
			}
			if hasJS {
				t.Fatalf("%s contains js, and its own language is not js", dir)
			}
		})
	}
	if tried == 0 {
		t.Fatal("no fixture is written in a language other than js")
	}
}
