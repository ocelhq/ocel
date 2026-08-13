package procgroup

import (
	"os"
	"os/exec"
)

func New(cmd *exec.Cmd) {
	newGroup(cmd)
}

func NewForeground(cmd *exec.Cmd, tty *os.File) {
	newForegroundGroup(cmd, tty)
}

func RestoreForeground(tty *os.File) error {
	return restoreForeground(tty)
}

func Terminate(cmd *exec.Cmd) error {
	return terminate(cmd)
}

func Kill(cmd *exec.Cmd) error {
	return kill(cmd)
}
