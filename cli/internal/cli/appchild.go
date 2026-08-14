package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/term"
)

const appChildGracePeriod = 2 * time.Second

const appChildWaitDelay = appChildGracePeriod + 3*time.Second

type appChild struct {
	cmd        *exec.Cmd
	done       chan struct{}
	err        chan error
	isTerminal bool
	term       termSnapshot
}

type termSnapshot struct {
	fd    int
	state *term.State
}

func snapshotTerminal(stdin io.Reader) termSnapshot {
	f, ok := stdin.(*os.File)
	if !ok {
		return termSnapshot{fd: -1}
	}
	fd := int(f.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return termSnapshot{fd: -1}
	}
	return termSnapshot{fd: fd, state: state}
}

func (s termSnapshot) restore() {
	if s.fd < 0 || s.state == nil {
		return
	}
	_ = term.Restore(s.fd, s.state)
}

func spawnAppChild(ctx context.Context, cmd *exec.Cmd, stdin io.Reader, isTerminal bool) (*appChild, error) {
	var snap termSnapshot
	if isTerminal {
		snap = snapshotTerminal(stdin)
		cmd.Cancel = func() error { return terminateAppTree(cmd) }
	} else {
		setNewProcessGroup(cmd)
		cmd.Cancel = func() error { return terminateProcessGroup(cmd) }
	}
	cmd.WaitDelay = appChildWaitDelay

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	errCh := make(chan error, 1)
	registerLiveAppChild(cmd, isTerminal, snap, done)
	go func() {
		err := cmd.Wait()
		snap.restore()
		errCh <- err
		close(done)
		deregisterLiveAppChild(cmd)
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
			if reaped(done) {
				return
			}
			_ = killAppChild(cmd, isTerminal)
			snap.restore()
		}
	}()

	return &appChild{cmd: cmd, done: done, err: errCh, isTerminal: isTerminal, term: snap}, nil
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
	if reaped(c.done) {
		c.term.restore()
		return
	}
	_ = killAppChild(c.cmd, c.isTerminal)
	c.term.restore()
	select {
	case <-c.done:
	case <-time.After(appChildGracePeriod):
	}
}

func reaped(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

type liveAppChild struct {
	cmd        *exec.Cmd
	isTerminal bool
	term       termSnapshot
	done       <-chan struct{}
}

var (
	liveAppChildrenMu sync.Mutex
	liveAppChildren   = map[*exec.Cmd]liveAppChild{}
)

func registerLiveAppChild(cmd *exec.Cmd, isTerminal bool, snap termSnapshot, done <-chan struct{}) {
	liveAppChildrenMu.Lock()
	liveAppChildren[cmd] = liveAppChild{cmd: cmd, isTerminal: isTerminal, term: snap, done: done}
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
		if !reaped(c.done) {
			_ = killAppChild(c.cmd, c.isTerminal)
		}
		c.term.restore()
	}
}
