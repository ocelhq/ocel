package image

import (
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/workspace"
)

type App struct {
	Slug       string
	Name       string
	Workspace  workspace.Location
	Dockerfile string
}

func (a App) Dir() string { return a.Workspace.Dir() }

func Describe(cfg *projectconfig.Config, app projectconfig.App) (App, error) {
	located, err := workspace.Locate(filepath.Join(cfg.Dir, app.Path))
	if err != nil {
		return App{}, fmt.Errorf("app %q: %w", app.Name, err)
	}
	described := App{Slug: cfg.Slug, Name: app.Name}
	if app.Build != nil {
		if app.Build.Context != "" {
			located, err = located.Rebase(filepath.Join(cfg.Dir, filepath.FromSlash(app.Build.Context)))
			if err != nil {
				return App{}, fmt.Errorf("app %q sets build.context to %q: %w", app.Name, app.Build.Context, err)
			}
		}
		located.BuildCommand = app.Build.Command
		described.Dockerfile = app.Build.Dockerfile
	}
	described.Workspace = located
	return described, nil
}
