package preflight

import (
	"context"
	"fmt"

	"github.com/ocelhq/ocel/cli/internal/imagebuild"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func RequireBuilder(ctx context.Context, cfg *projectconfig.Config) error {
	var containers []string
	for _, app := range cfg.Apps {
		if app.Compute == string(providerkit.ComputeContainer) {
			containers = append(containers, app.Name)
		}
	}
	if len(containers) == 0 {
		return nil
	}
	if err := imagebuild.Reachable(ctx); err != nil {
		return fmt.Errorf("building the container image for %s happens on this machine, before anything is provisioned:\n    %w", list(containers), err)
	}
	return nil
}
