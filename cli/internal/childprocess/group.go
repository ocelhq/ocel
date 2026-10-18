package childprocess

import "os/exec"

func SetOwnGroup(cmd *exec.Cmd) {
	setOwnGroup(cmd)
}

func TerminateGroup(cmd *exec.Cmd) error {
	return terminateGroup(cmd)
}

func KillGroup(cmd *exec.Cmd) error {
	return killGroup(cmd)
}
