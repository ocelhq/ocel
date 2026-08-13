package cli

import (
	"context"

	"github.com/ocelhq/ocel/cli/internal/deployui"
	"github.com/ocelhq/ocel/cli/internal/obs"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func startRun(ctx context.Context, cfg *projectconfig.Config, command string) (context.Context, *obs.Run, error) {
	return obs.Start(ctx, cfg.Dir, command)
}

func sessionFormat() deployui.Format {
	if logFormat() == logFormatJSON {
		return deployui.FormatJSON
	}
	return deployui.FormatHuman
}
