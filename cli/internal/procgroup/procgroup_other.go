//go:build !unix

package procgroup

import "os/exec"

func newGroup(cmd *exec.Cmd) {}

func terminate(cmd *exec.Cmd) error {
	return nil
}

func kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
