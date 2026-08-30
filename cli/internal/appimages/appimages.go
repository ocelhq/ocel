package appimages

import (
	"context"
	"io"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func Build(ctx context.Context, cfg *projectconfig.Config, progress io.Writer) (map[string]string, error) {
	var refs map[string]string
	for _, app := range Apps(cfg) {
		image, err := imagebuild.Builder{Progress: progress}.Build(ctx, imagebuild.App{
			Name:       app.Name,
			Dir:        filepath.Join(cfg.Dir, app.Path),
			Configured: DockerfileOf(app),
		})
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
