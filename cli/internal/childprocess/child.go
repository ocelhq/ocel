package childprocess

import (
	"cmp"
	"context"
	"fmt"
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
	Reap     time.Duration
}

type Child struct {
	cmd        *exec.Cmd
	group      *processGroup
	done       chan struct{}
	exited     chan error
	isTerminal bool
	terminal   terminalState
	grace      time.Duration
	reap       time.Duration
}

func Start(ctx context.Context, cmd *exec.Cmd, opts Options) (*Child, error) {
	child := &Child{
		cmd:        cmd,
		done:       make(chan struct{}),
		exited:     make(chan error, 1),
		isTerminal: opts.Terminal,
		grace:      cmp.Or(opts.Grace, GracePeriod),
		reap:       cmp.Or(opts.Reap, opts.Grace, GracePeriod),
	}
	if cmd.Cancel != nil {
		return nil, fmt.Errorf("start %s: it was made with exec.CommandContext, and only Start may stop it when its context ends", cmd.Path)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	group, err := newProcessGroup(cmd, !opts.Terminal)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	child.group = group
	if opts.Terminal {
		child.terminal = saveTerminal(opts.Stdin)
	}
	cmd.WaitDelay = WaitDelay

	if err := cmd.Start(); err != nil {
		group.release()
		child.terminal.restore()
		return nil, err
	}
	if err := group.attach(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		group.release()
		child.terminal.restore()
		return nil, fmt.Errorf("start %s: %w", cmd.Path, err)
	}
	running.add(child)
	go func() {
		err := cmd.Wait()
		if !child.isTerminal {
			_ = group.kill(cmd)
		}
		group.release()
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
	case <-time.After(c.reap):
	}
}

func (c *Child) terminate() error {
	return c.group.terminate(c.cmd)
}

func (c *Child) kill() error {
	return c.group.kill(c.cmd)
}

func (c *Child) hasExited() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
