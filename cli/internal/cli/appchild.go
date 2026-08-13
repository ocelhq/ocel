package cli

import (
	"context"
	"io"
	"os/exec"
	"sync"
	"time"
)

const appChildGracePeriod = 2 * time.Second

const appChildWaitDelay = appChildGracePeriod + 3*time.Second

type appChild struct {
	cmd        *exec.Cmd
	done       chan struct{}
	err        chan error
	isTerminal bool
}

func spawnAppChild(ctx context.Context, cmd *exec.Cmd, stdin io.Reader, isTerminal bool) (*appChild, error) {
	if isTerminal {
		cmd.Cancel = func() error { return terminateAppTree(cmd) }
	} else {
		setNewProcessGroup(cmd)
		cmd.Cancel = func() error { return terminateProcessGroup(cmd) }
	}
	cmd.WaitDelay = appChildWaitDelay

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	registerLiveAppChild(cmd, isTerminal)

	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		deregisterLiveAppChild(cmd)
		errCh <- err
		close(done)
	}()

	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		select {
		case <-done:
		case <-time.After(appChildGracePeriod):
			_ = killAppChild(cmd, isTerminal)
		}
	}()

	return &appChild{cmd: cmd, done: done, err: errCh, isTerminal: isTerminal}, nil
}

func killAppChild(cmd *exec.Cmd, isTerminal bool) error {
	if isTerminal {
		return killAppTree(cmd)
	}
	return killProcessGroup(cmd)
}

func (c *appChild) wait() error {
	return <-c.err
}

func (c *appChild) stop() {
	_ = killAppChild(c.cmd, c.isTerminal)
	select {
	case <-c.done:
	case <-time.After(appChildGracePeriod):
	}
}

type liveAppChild struct {
	cmd        *exec.Cmd
	isTerminal bool
}

var (
	liveAppChildrenMu sync.Mutex
	liveAppChildren   = map[*exec.Cmd]liveAppChild{}
)

func registerLiveAppChild(cmd *exec.Cmd, isTerminal bool) {
	liveAppChildrenMu.Lock()
	liveAppChildren[cmd] = liveAppChild{cmd: cmd, isTerminal: isTerminal}
	liveAppChildrenMu.Unlock()
}

func deregisterLiveAppChild(cmd *exec.Cmd) {
	liveAppChildrenMu.Lock()
	delete(liveAppChildren, cmd)
	liveAppChildrenMu.Unlock()
}

func killAllLiveAppChildren() {
	liveAppChildrenMu.Lock()
	children := make([]liveAppChild, 0, len(liveAppChildren))
	for _, c := range liveAppChildren {
		children = append(children, c)
	}
	liveAppChildrenMu.Unlock()

	for _, c := range children {
		_ = killAppChild(c.cmd, c.isTerminal)
	}
}
