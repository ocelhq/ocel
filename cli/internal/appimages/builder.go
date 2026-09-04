package appimages

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/runui"
)

func RequireBuilder(ctx context.Context, rep runui.Reporter, cfg *projectconfig.Config) error {
	var chosen []imagebuild.Choice
	for _, app := range Apps(cfg) {
		built, err := Describe(cfg, app)
		if err != nil {
			return err
		}
		choice, err := imagebuild.Choose(built)
		if err != nil {
			return err
		}
		chosen = append(chosen, choice)
	}
	if len(chosen) == 0 {
		return nil
	}
	containers := make([]string, len(chosen))
	for i, choice := range chosen {
		containers[i] = choice.App.Name
		if notice := choice.Notice(); notice != "" {
			rep.Diagnostic(notice)
		}
	}
	if err := imagebuild.Reachable(ctx); err != nil {
		return fmt.Errorf("building the container image for %s happens on this machine, before anything is provisioned:\n    %w", runui.Quoted(containers), err)
	}
	return nil
}
