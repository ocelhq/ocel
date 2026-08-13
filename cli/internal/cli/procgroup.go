package cli

import (
	"os"
	"os/exec"

	"github.com/ocelhq/ocel/cli/internal/procgroup"
)

func setNewForegroundProcessGroup(cmd *exec.Cmd, tty *os.File) {
	procgroup.NewForeground(cmd, tty)
}

func restoreForegroundProcessGroup(tty *os.File) error {
	return procgroup.RestoreForeground(tty)
}

func killProcessGroup(cmd *exec.Cmd) error {
	return procgroup.Kill(cmd)
}

func terminateProcessGroup(cmd *exec.Cmd) error {
	return procgroup.Terminate(cmd)
}
