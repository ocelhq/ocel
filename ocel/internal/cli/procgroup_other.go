//go:build !unix

package cli

import "os/exec"

// setNewProcessGroup is a no-op on platforms without POSIX process groups.
func setNewProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills just cmd itself; without a process group there's
// no portable way to also kill its descendants.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
