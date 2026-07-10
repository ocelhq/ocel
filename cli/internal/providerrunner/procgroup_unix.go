//go:build unix

package providerrunner

import (
	"os/exec"
	"syscall"
)

// setNewProcessGroup puts cmd in its own process group so terminateProcessGroup
// and killProcessGroup can later stop it and every process it spawns, not
// just cmd itself.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateProcessGroup sends SIGTERM to cmd's entire process group, giving
// it a chance to shut down cleanly before killProcessGroup is used.
func terminateProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// killProcessGroup forcibly kills cmd's entire process group.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
