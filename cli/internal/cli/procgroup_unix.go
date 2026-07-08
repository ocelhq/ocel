//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

// setNewProcessGroup puts cmd in its own process group so killProcessGroup
// can later stop it and every process it spawns (e.g. a dev server that
// forks its own children), not just cmd itself.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills cmd's entire process group.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
