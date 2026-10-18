//go:build unix

package providerprocess

import (
	"os/exec"
	"syscall"
)

func leaveTheProvidersGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
