package appimages

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/workspace"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func Build(ctx context.Context, cfg *projectconfig.Config, phase string, progress io.Writer) (map[string]string, error) {
	var refs map[string]string
	for _, app := range Apps(cfg) {
		built, err := Describe(cfg, app)
		if err != nil {
			return nil, err
		}
		image, err := imagebuild.Builder{Progress: progress, Phase: phase}.Build(ctx, built)
		if err != nil {
			return nil, err
		}
		if refs == nil {
			refs = map[string]string{}
		}
		refs[app.Name] = image.Ref
	}
	return refs, nil
}

func Describe(cfg *projectconfig.Config, app projectconfig.App) (imagebuild.App, error) {
	located, err := workspace.Locate(filepath.Join(cfg.Dir, app.Path))
	if err != nil {
		return imagebuild.App{}, fmt.Errorf("app %q: %w", app.Name, err)
	}
	if app.Build != nil {
		if app.Build.Context != "" {
			located, err = located.Rebase(filepath.Join(cfg.Dir, filepath.FromSlash(app.Build.Context)))
			if err != nil {
				return imagebuild.App{}, fmt.Errorf("app %q sets build.context to %q: %w", app.Name, app.Build.Context, err)
			}
		}
		located.BuildCommand = app.Build.Command
	}
	return imagebuild.App{Slug: cfg.Slug, Name: app.Name, Workspace: located, Configured: DockerfileOf(app)}, nil
}

func Apps(cfg *projectconfig.Config) []projectconfig.App {
	var containers []projectconfig.App
	for _, app := range cfg.Apps {
		if app.Compute == string(providerkit.ComputeContainer) {
			containers = append(containers, app)
		}
	}
	return containers
}

func DockerfileOf(app projectconfig.App) string {
	if app.Build == nil {
		return ""
	}
	return app.Build.Dockerfile
}
