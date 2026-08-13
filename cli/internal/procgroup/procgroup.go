// Package procgroup manages an *exec.Cmd as the leader of its own process
// group, so its whole tree — including grandchildren it spawns — can be
// signaled together instead of just the direct child.
package procgroup

import "os/exec"

// New arranges for cmd to start as the leader of a new process group. Call
// it before cmd.Start(); Terminate and Kill then reach everything the
// resulting tree spawns, not just cmd's direct child.
func New(cmd *exec.Cmd) {
	newGroup(cmd)
}

// Terminate sends SIGTERM to cmd's process group. It is a no-op returning
// nil if cmd never started or its group is already gone.
func Terminate(cmd *exec.Cmd) error {
	return terminate(cmd)
}

// Kill sends SIGKILL to cmd's process group. It is a no-op returning nil if
// cmd never started or its group is already gone.
func Kill(cmd *exec.Cmd) error {
	return kill(cmd)
}
