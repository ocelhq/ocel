package procgroup

import "os/exec"

func New(cmd *exec.Cmd) {
	newGroup(cmd)
}

func Terminate(cmd *exec.Cmd) error {
	return terminate(cmd)
}

func Kill(cmd *exec.Cmd) error {
	return kill(cmd)
}
