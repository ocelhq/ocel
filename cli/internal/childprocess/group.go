package childprocess

import (
	"os/exec"
	"time"
)

const GroupWaitDelay = 5 * time.Second

func SetOwnGroup(cmd *exec.Cmd) {
	setOwnGroup(cmd)
}

type Group struct {
	cmd *exec.Cmd
}

func StartGroup(cmd *exec.Cmd) (*Group, error) {
	SetOwnGroup(cmd)
	cmd.Cancel = func() error { return KillGroup(cmd) }
	cmd.WaitDelay = GroupWaitDelay
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	group := &Group{cmd: cmd}
	running.addGroup(group)
	return group, nil
}

func RunGroup(cmd *exec.Cmd) error {
	group, err := StartGroup(cmd)
	if err != nil {
		return err
	}
	return group.Wait()
}

func (g *Group) Wait() error {
	defer running.removeGroup(g)
	return g.cmd.Wait()
}

func TerminateGroup(cmd *exec.Cmd) error {
	return terminateGroup(cmd)
}

func KillGroup(cmd *exec.Cmd) error {
	return killGroup(cmd)
}
