package workspace_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/pythonserver"
	"github.com/ocelhq/ocel/cli/internal/workspace"
)

func TestAPythonAppStartsItsResolvedEntrypointThroughTheServerProgramItsBuildWrites(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "api", "main.py"), []byte("app = None\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for entrypoint, start := range map[string]string{
		"main.py":      "cd api && python3 ocel-server.py file main.py",
		"shop.api:app": "cd api && python3 ocel-server.py module shop.api:app",
	} {
		loc := workspace.Location{Root: root, Path: "api", Language: language.Python, Entrypoint: entrypoint}
		commands, err := loc.Commands()
		if err != nil {
			t.Fatalf("Commands(%q) = %v", entrypoint, err)
		}
		want := workspace.Commands{Start: start, Asset: workspace.Asset{Name: pythonserver.File, Content: pythonserver.Source}}
		if commands != want {
			t.Errorf("Commands(%q) starts %q writing %q, want %q writing the server program", entrypoint, commands.Start, commands.Asset.Name, start)
		}
	}
}

func TestAPythonAppWithNoEntrypointIsLeftToRailpacksOwnStartCommand(t *testing.T) {
	loc := workspace.Location{Root: t.TempDir(), Path: ".", Language: language.Python}

	commands, err := loc.Commands()
	if err != nil || commands != (workspace.Commands{}) {
		t.Errorf("Commands() = %+v, %v, want nothing: with no entrypoint resolved, railpack decides how the image starts, as it does for a Django project", commands, err)
	}
}

func TestAPythonAppInADirectoryWithASpaceStartsInThatDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "my api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "my api", "main.py"), []byte("app = None\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loc := workspace.Location{Root: root, Path: "my api", Language: language.Python, Entrypoint: "main.py"}

	commands, err := loc.Commands()
	if want := "cd 'my api' && python3 ocel-server.py file main.py"; err != nil || commands.Start != want {
		t.Errorf("Commands() = %+v, %v, want the start %q", commands, err, want)
	}
}
