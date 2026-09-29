//go:build !unix

package childprocess

import (
	"errors"
	"os"
	"os/exec"
)

func setOwnGroup(cmd *exec.Cmd) {}

func terminateGroup(cmd *exec.Cmd) error {
	return killGroup(cmd)
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrPermission) {
		return os.ErrProcessDone
	}
	return err
}
