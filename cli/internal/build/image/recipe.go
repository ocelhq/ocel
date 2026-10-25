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
			return Recipe{}, fmt.Errorf("app %q sets image.dockerfile to %q, and %s is not a file", app.Name, app.Dockerfile, path)
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
		return fmt.Sprintf("%s builds from %s, with context %s", r.App.Name, r.Dockerfile, r.App.Workspace.Root)
	case r.App.Workspace.Member:
		return fmt.Sprintf("%s builds from %s, with the workspace root %s as context", r.App.Name, r.Dockerfile, r.App.Workspace.Root)
	case r.App.Workspace.ModuleDir != "" && r.App.Workspace.Path != ".":
		return fmt.Sprintf("%s builds from %s, with context %s, which holds its Go module and every module it replaces", r.App.Name, r.Dockerfile, r.App.Workspace.Root)
	default:
		return fmt.Sprintf("%s builds from %s", r.App.Name, r.Dockerfile)
	}
}
