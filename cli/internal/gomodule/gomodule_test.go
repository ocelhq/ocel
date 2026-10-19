package gomodule_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/gomodule"
)

func writeModule(t *testing.T, dir, path string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, gomodule.File), []byte("module "+path+"\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindInProjectLooksFromTheAppUpToTheProjectRootAndNoHigher(t *testing.T) {
	outer := t.TempDir()
	writeModule(t, outer, "example.com/monorepo")
	project := filepath.Join(outer, "project")
	app := filepath.Join(project, "services", "api")
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}

	if got, found, err := gomodule.FindInProject(app, project); err != nil || found {
		t.Errorf("FindInProject() = %+v, %v, %v, want no module — the go.mod above the project root is not the project's", got, found, err)
	}

	writeModule(t, project, "example.com/shop")
	got, found, err := gomodule.FindInProject(app, project)
	if err != nil || !found || got.Dir != project || got.Path != "example.com/shop" {
		t.Errorf("FindInProject() = %+v, %v, %v, want the module at the project root %s", got, found, err, project)
	}
}

func TestFindInProjectLooksOnlyInTheAppsOwnDirectoryWhenItLiesOutsideTheProject(t *testing.T) {
	outer := t.TempDir()
	writeModule(t, outer, "example.com/monorepo")
	project := filepath.Join(outer, "project")
	app := filepath.Join(outer, "elsewhere", "api")
	for _, dir := range []string{project, app} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if got, found, err := gomodule.FindInProject(app, project); err != nil || found {
		t.Errorf("FindInProject() = %+v, %v, %v, want no module — an app outside the project is a module only in its own directory", got, found, err)
	}

	writeModule(t, app, "example.com/api")
	if got, found, err := gomodule.FindInProject(app, project); err != nil || !found || got.Dir != app {
		t.Errorf("FindInProject() = %+v, %v, %v, want the module in the app's own directory %s", got, found, err, app)
	}
}

func TestPackagePathAddressesAnAppsPackageFromTheModuleItBelongsTo(t *testing.T) {
	module := t.TempDir()
	for _, dir := range []string{"services/api/cmd/api"} {
		if err := os.MkdirAll(filepath.Join(module, filepath.FromSlash(dir)), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, tt := range []struct {
		app, entrypoint, want string
	}{
		{".", "", "."},
		{".", ".", "."},
		{"services/api", "cmd/api", "./services/api/cmd/api"},
		{"services/api/cmd/api", ".", "./services/api/cmd/api"},
	} {
		got, err := gomodule.PackagePath(module, filepath.Join(module, filepath.FromSlash(tt.app)), tt.entrypoint)
		if err != nil || got != tt.want {
			t.Errorf("PackagePath(%s, %q) = %q, %v, want %s", tt.app, tt.entrypoint, got, err, tt.want)
		}
	}
}

func TestPackagePathRefusesAnEntrypointOutsideTheModule(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "web"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := gomodule.PackagePath(filepath.Join(dir, "api"), filepath.Join(dir, "api"), "../cmd/web")
	if err == nil || !strings.Contains(err.Error(), "../cmd/web") {
		t.Errorf("PackagePath() = %v, want a refusal naming the entrypoint ../cmd/web outside the module", err)
	}
}

func TestPackagePathRefusesAnEntrypointNamingAFileRatherThanADirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := gomodule.PackagePath(dir, dir, "main.go"); err == nil || !strings.Contains(err.Error(), `"main.go"`) {
		t.Errorf("PackagePath() = %v, want a refusal naming the entrypoint main.go, a file", err)
	}
}
