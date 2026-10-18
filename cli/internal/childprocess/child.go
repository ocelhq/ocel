package childprocess

import (
	"cmp"
	"context"
	"io"
	"os/exec"
	"time"
)

const GracePeriod = 2 * time.Second

const WaitDelay = GracePeriod + 3*time.Second

type Options struct {
	Stdin    io.Reader
	Terminal bool
	Grace    time.Duration
}

type Child struct {
	cmd        *exec.Cmd
	done       chan struct{}
	exited     chan error
	isTerminal bool
	terminal   terminalState
	grace      time.Duration
}

func Start(ctx context.Context, cmd *exec.Cmd, opts Options) (*Child, error) {
	child := &Child{
		cmd:        cmd,
		done:       make(chan struct{}),
		exited:     make(chan error, 1),
		isTerminal: opts.Terminal,
		grace:      cmp.Or(opts.Grace, GracePeriod),
	}
	if opts.Terminal {
		child.terminal = saveTerminal(opts.Stdin)
	} else {
		SetOwnGroup(cmd)
	}
	if cmd.Cancel != nil {
		cmd.Cancel = child.terminate
	}
	cmd.WaitDelay = WaitDelay

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	running.add(child)
	go func() {
		err := cmd.Wait()
		if !child.isTerminal {
			_ = KillGroup(cmd)
		}
		child.terminal.restore()
		child.exited <- err
		close(child.done)
		running.remove(child)
	}()

	go func() {
		select {
		case <-ctx.Done():
			child.Stop()
		case <-child.done:
		}
	}()

	return child, nil
}

func Run(ctx context.Context, cmd *exec.Cmd) error {
	child, err := Start(ctx, cmd, Options{})
	if err != nil {
		return err
	}
	return child.Wait()
}

func (c *Child) Exited() <-chan error {
	return c.exited
}

func (c *Child) Wait() error {
	return <-c.exited
}

func (c *Child) Stop() {
	defer c.terminal.restore()
	if c.hasExited() {
		return
	}
	_ = c.terminate()
	select {
	case <-c.done:
		return
	case <-time.After(c.grace):
	}
	_ = c.kill()
	select {
	case <-c.done:
	case <-time.After(c.grace):
	}
}

func (c *Child) terminate() error {
	if c.isTerminal {
		return terminateTree(c.cmd)
	}
	return TerminateGroup(c.cmd)
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
