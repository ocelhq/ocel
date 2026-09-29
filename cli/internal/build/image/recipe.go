package image

import (
	"fmt"
	"os"
	"path/filepath"
)

const DockerfileName = "Dockerfile"

type Recipe struct {
	App        App
	Dockerfile string
}

func ChooseRecipe(app App) (Recipe, error) {
	dir := app.Dir()
	if app.Dockerfile != "" {
		path := app.Dockerfile
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return Recipe{}, fmt.Errorf("app %q sets build.dockerfile to %q, and %s is not a file to build from: build.dockerfile resolves against the app's own directory, and may point outside it", app.Name, app.Dockerfile, path)
		}
		return Recipe{App: app, Dockerfile: path}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Recipe{}, fmt.Errorf("read the directory app %q is built from: %w", app.Name, err)
	}
	for _, entry := range entries {
		if entry.Name() != DockerfileName {
			continue
		}
		beside := filepath.Join(dir, DockerfileName)
		if info, err := os.Stat(beside); err == nil && info.Mode().IsRegular() {
			return Recipe{App: app, Dockerfile: beside}, nil
		}
	}
	return Recipe{App: app}, nil
}

func (r Recipe) Notice() string {
	switch {
	case r.Dockerfile == "":
		return ""
	case r.App.Dockerfile != "":
		return fmt.Sprintf("%s builds from %s, the build.dockerfile it names — its build context is still %s", r.App.Name, r.Dockerfile, r.App.Workspace.Root)
	case r.App.Workspace.Member:
		return fmt.Sprintf("%s builds from the %s beside it rather than with railpack, and copies from the workspace root %s, which is its build context — rename or remove %s to go back", r.App.Name, DockerfileName, r.App.Workspace.Root, r.Dockerfile)
	default:
		return fmt.Sprintf("%s builds from the %s beside it rather than with railpack — rename or remove %s to go back", r.App.Name, DockerfileName, r.Dockerfile)
	}
}
