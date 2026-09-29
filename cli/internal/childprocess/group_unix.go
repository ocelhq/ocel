//go:build unix

package childprocess

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func setOwnGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func terminateGroup(cmd *exec.Cmd) error {
	return signalGroup(cmd, syscall.SIGTERM)
}

func killGroup(cmd *exec.Cmd) error {
	return signalGroup(cmd, syscall.SIGKILL)
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
