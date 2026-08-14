package procgroup

import (
	"os/exec"
	"time"
)

const GuardWaitDelay = 5 * time.Second

func New(cmd *exec.Cmd) {
	newGroup(cmd)
}

func Guard(cmd *exec.Cmd) {
	New(cmd)
	cmd.Cancel = func() error { return Kill(cmd) }
	cmd.WaitDelay = GuardWaitDelay
}

func Terminate(cmd *exec.Cmd) error {
	return terminate(cmd)
}

func Kill(cmd *exec.Cmd) error {
	return kill(cmd)
}
