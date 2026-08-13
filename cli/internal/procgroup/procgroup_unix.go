//go:build unix

package procgroup

import (
	"errors"
	"os/exec"
	"syscall"
)

func newGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func terminate(cmd *exec.Cmd) error {
	return signalGroup(cmd, syscall.SIGTERM)
}

func kill(cmd *exec.Cmd) error {
	return signalGroup(cmd, syscall.SIGKILL)
}

// signalGroup targets the negative pgid, which addresses the whole group
// rather than just cmd's direct child. ESRCH means the group is already
// gone, which is the outcome being asked for, not a failure worth surfacing
// — callers that plug this into exec.Cmd.Cancel need a nil (or
// os.ErrProcessDone) return there, since os/exec treats any other error as
// a reason to skip its own Process.Kill() fallback and leave the pipes open.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
