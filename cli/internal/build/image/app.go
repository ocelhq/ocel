package image

import (
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/workspace"
)

type App struct {
	Slug       string
	Name       string
	Workspace  workspace.Location
	Dockerfile string
}

func (a App) Dir() string { return a.Workspace.Dir() }

func Describe(cfg *project.Project, app project.App) (App, error) {
	located, err := workspace.Locate(filepath.Join(cfg.Dir, app.Path))
	if err != nil {
		return App{}, fmt.Errorf("app %q: %w", app.Name, err)
	}
	described := App{Slug: cfg.Slug, Name: app.Name}
	if app.Container != nil && app.Container.Build != nil {
		build := app.Container.Build
		if build.Context != "" {
			located, err = located.Rebase(filepath.Join(cfg.Dir, filepath.FromSlash(build.Context)))
			if err != nil {
				return App{}, fmt.Errorf("app %q sets image.context to %q: %w", app.Name, build.Context, err)
			}
		}
		located.BuildCommand = build.Command
		described.Dockerfile = build.Dockerfile
	}
	described.Workspace = located
	return described, nil
}
