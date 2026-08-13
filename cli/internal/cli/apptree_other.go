//go:build !unix

package cli

import "os/exec"

func terminateAppTree(cmd *exec.Cmd) error {
	return terminateProcessGroup(cmd)
}

func killAppTree(cmd *exec.Cmd) error {
	return killProcessGroup(cmd)
}
