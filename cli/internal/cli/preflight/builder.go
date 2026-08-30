package preflight

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runui"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func RequireBuilder(ctx context.Context, rep runui.Reporter, cfg *projectconfig.Config) error {
	var containers []string
	var chosen []imagebuild.Choice
	for _, app := range cfg.Apps {
		if app.Compute != string(providerkit.ComputeContainer) {
			continue
		}
		containers = append(containers, app.Name)
		choice, err := imagebuild.Choose(imagebuild.App{
			Name:       app.Name,
			Dir:        filepath.Join(cfg.Dir, app.Path),
			Configured: dockerfileOf(app),
		})
		if err != nil {
			return err
		}
		chosen = append(chosen, choice)
	}
	if len(containers) == 0 {
		return nil
	}
	for _, choice := range chosen {
		if notice := choice.Notice(); notice != "" {
			rep.Diagnostic(notice)
		}
	}
	if err := imagebuild.Reachable(ctx); err != nil {
		return fmt.Errorf("building the container image for %s happens on this machine, before anything is provisioned:\n    %w", list(containers), err)
	}
	return nil
}

func dockerfileOf(app projectconfig.App) string {
	if app.Build == nil {
		return ""
	}
	return app.Build.Dockerfile
}
