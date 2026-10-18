package lifecycle

import (
	"os/exec"
	"syscall"
)

func newShellCommand(line string) *exec.Cmd {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + line + `"`}
	return cmd
}
