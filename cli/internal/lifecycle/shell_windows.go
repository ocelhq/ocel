package lifecycle

import (
	"context"
	"syscall"

	"github.com/ocelhq/ocel/pkg/tracing"
)

func newShellCommand(ctx context.Context, line string) *tracing.Cmd {
	cmd := tracing.Command(ctx, "cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + line + `"`}
	return cmd
}
