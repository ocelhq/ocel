//go:build !windows

package lifecycle

import (
	"context"
	"os/exec"
)

func newShellCommand(ctx context.Context, line string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", line)
}
