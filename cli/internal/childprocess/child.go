package childprocess

import (
	"context"
	"io"
	"os/exec"
	"time"
)

const GracePeriod = 2 * time.Second

const WaitDelay = GracePeriod + 3*time.Second

type Child struct {
	cmd        *exec.Cmd
	done       chan struct{}
	exited     chan error
	isTerminal bool
	terminal   terminalState
}

func Start(ctx context.Context, cmd *exec.Cmd, stdin io.Reader, isTerminal bool) (*Child, error) {
	var terminal terminalState
	if isTerminal {
		terminal = saveTerminal(stdin)
		cmd.Cancel = func() error { return terminateTree(cmd) }
	} else {
		SetOwnGroup(cmd)
		cmd.Cancel = func() error { return TerminateGroup(cmd) }
	}
	cmd.WaitDelay = WaitDelay

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &Child{cmd: cmd, done: make(chan struct{}), exited: make(chan error, 1), isTerminal: isTerminal, terminal: terminal}
	running.add(child)
	go func() {
		err := cmd.Wait()
		terminal.restore()
		child.exited <- err
		close(child.done)
		running.remove(child)
	}()

	go func() {
		select {
		case <-ctx.Done():
		case <-child.done:
			return
		}
		select {
		case <-child.done:
		case <-time.After(GracePeriod):
			if child.hasExited() {
				return
			}
			_ = child.kill()
			terminal.restore()
		}
	}()

	return child, nil
}

func (c *Child) Exited() <-chan error {
	return c.exited
}

func (c *Child) Wait() error {
	return <-c.exited
}

func (c *Child) Stop() {
	if c.hasExited() {
		c.terminal.restore()
		return
	}
	_ = c.kill()
	c.terminal.restore()
	select {
	case <-c.done:
	case <-time.After(GracePeriod):
	}
}

func (c *Child) kill() error {
	if c.isTerminal {
		return killTree(c.cmd)
	}
	return KillGroup(c.cmd)
}

func (c *Child) hasExited() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
