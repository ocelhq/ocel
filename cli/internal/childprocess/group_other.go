//go:build !unix && !windows

package childprocess

import (
	"errors"
	"os"
	"os/exec"
)

type processGroup struct{}

func newProcessGroup(*exec.Cmd, bool) (*processGroup, error) {
	return &processGroup{}, nil
}

func (*processGroup) attach(*exec.Cmd) error { return nil }

func (*processGroup) release() {}

func (g *processGroup) terminate(cmd *exec.Cmd) error {
	return g.kill(cmd)
}

func (*processGroup) kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := cmd.Process.Kill()
	if errors.Is(err, os.ErrPermission) {
		return os.ErrProcessDone
	}
	return err
}
