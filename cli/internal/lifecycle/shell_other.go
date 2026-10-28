//go:build !windows

package lifecycle

import (
	"context"

	"github.com/ocelhq/ocel/pkg/tracing"
)

func newShellCommand(ctx context.Context, line string) *tracing.Cmd {
	return tracing.Command(ctx, "sh", "-c", line)
}
