package childprocess

import (
	"os/exec"
	"time"
)

const GroupWaitDelay = 5 * time.Second

func SetOwnGroup(cmd *exec.Cmd) {
	setOwnGroup(cmd)
}

func KillGroupOnCancel(cmd *exec.Cmd) {
	SetOwnGroup(cmd)
	cmd.Cancel = func() error { return KillGroup(cmd) }
	cmd.WaitDelay = GroupWaitDelay
}

func TerminateGroup(cmd *exec.Cmd) error {
	return terminateGroup(cmd)
}

func KillGroup(cmd *exec.Cmd) error {
	return killGroup(cmd)
}
