//go:build !unix

package procgroup

import (
	"errors"
	"os"
	"os/exec"
)

func newGroup(cmd *exec.Cmd) {}

func newForegroundGroup(cmd *exec.Cmd, tty *os.File) {
	newGroup(cmd)
}

func restoreForeground(tty *os.File) error {
	return nil
}

func terminate(cmd *exec.Cmd) error {
	return kill(cmd)
}

func kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrPermission) {
		return os.ErrProcessDone
	}
	return err
}
