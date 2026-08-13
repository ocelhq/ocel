//go:build !unix

package procgroup

import "os/exec"

func newGroup(cmd *exec.Cmd) {}

// terminate has no non-unix equivalent to SIGTERM-then-grace, so it is a
// no-op here as it was before this package existed.
func terminate(cmd *exec.Cmd) error {
	return nil
}

// kill matches what os/exec's default Cancel does off unix, since there is
// no process group to target.
func kill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
