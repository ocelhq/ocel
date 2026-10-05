//go:build unix

package childprocess

import (
	"os/exec"
	"syscall"
)

func StartDetached(newCmd func() *exec.Cmd) error {
	cmd := newCmd()
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	return startReleased(cmd)
}
