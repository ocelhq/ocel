//go:build !windows

package lifecycle

import "os/exec"

type processTree struct{}

func newProcessTree(*exec.Cmd) (*processTree, error) {
	return &processTree{}, nil
}

func (*processTree) assign(int) error {
	return nil
}

func (*processTree) kill() {}
