//go:build unix

package childprocess

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type processGroup struct {
	own bool
}

func newProcessGroup(cmd *exec.Cmd, own bool) (*processGroup, error) {
	if own {
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.Setpgid = true
	}
	return &processGroup{own: own}, nil
}

func (*processGroup) attach(*exec.Cmd) error { return nil }

func (*processGroup) release() {}

func (g *processGroup) terminate(cmd *exec.Cmd) error {
	if !g.own {
		return terminateTree(cmd)
	}
	return signalGroup(cmd, syscall.SIGTERM)
}

func (g *processGroup) kill(cmd *exec.Cmd) error {
	if !g.own {
		return killTree(cmd)
	}
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
