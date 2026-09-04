package imagebuild

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/workspace"
)

const DockerfileName = "Dockerfile"

type App struct {
	Name       string
	Workspace  workspace.Location
	Configured string
}

func (a App) Dir() string {
	return filepath.Join(a.Workspace.Root, filepath.FromSlash(a.Workspace.Path))
}

type Choice struct {
	App        App
	Dockerfile string
}

func Choose(app App) (Choice, error) {
	dir := app.Dir()
	if app.Configured != "" {
		path := app.Configured
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return Choice{}, fmt.Errorf("app %q sets build.dockerfile to %q, and %s is not a file to build from: build.dockerfile resolves against the app's own directory, and may point outside it", app.Name, app.Configured, path)
		}
		return Choice{App: app, Dockerfile: path}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Choice{}, fmt.Errorf("read the directory app %q is built from: %w", app.Name, err)
	}
	for _, entry := range entries {
		if entry.Name() != DockerfileName {
			continue
		}
		beside := filepath.Join(dir, DockerfileName)
		if info, err := os.Stat(beside); err == nil && info.Mode().IsRegular() {
			return Choice{App: app, Dockerfile: beside}, nil
		}
	}
	return Choice{App: app}, nil
}

func (c Choice) Notice() string {
	switch {
	case c.Dockerfile == "":
		return ""
	case c.App.Configured != "":
		return fmt.Sprintf("%s builds from %s, the build.dockerfile it names — its build context is still %s", c.App.Name, c.Dockerfile, c.App.Workspace.Root)
	default:
		return fmt.Sprintf("%s builds from the %s beside it rather than with railpack — rename or remove %s to go back", c.App.Name, DockerfileName, c.Dockerfile)
	}
}
