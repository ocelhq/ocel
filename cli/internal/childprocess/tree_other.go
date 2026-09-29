//go:build !unix

package childprocess

import "os/exec"

func terminateTree(cmd *exec.Cmd) error {
	return TerminateGroup(cmd)
}

func killTree(cmd *exec.Cmd) error {
	return KillGroup(cmd)
}
