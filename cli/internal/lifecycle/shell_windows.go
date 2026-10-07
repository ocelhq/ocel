package lifecycle

import (
	"context"
	"os/exec"
	"syscall"
)

func newShellCommand(ctx context.Context, line string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + line + `"`}
	return cmd
}
