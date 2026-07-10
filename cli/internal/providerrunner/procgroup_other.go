//go:build !unix

package providerrunner

import "os/exec"

// setNewProcessGroup is a no-op on platforms without POSIX process groups.
func setNewProcessGroup(cmd *exec.Cmd) {}

// terminateProcessGroup is a no-op on platforms without POSIX process
// groups; without SIGTERM support, teardown falls straight through to
// killProcessGroup once the grace period elapses.
func terminateProcessGroup(cmd *exec.Cmd) error {
	return nil
}

// killProcessGroup kills just cmd itself; without a process group there's no
// portable way to also kill its descendants.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
