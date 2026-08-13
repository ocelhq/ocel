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

// termSnapshot is stdin's termios as it stood before an app child on the tty
// path ran — never a raw mode the CLI put it in itself, since GetState only
// reads. The tty path hands the app child the real terminal (see
// spawnAppChild's isTerminal branch), so a dev server that legitimately
// raw-modes it (any interactive keypress UI) can leave it that way if killed
// before it gets to restore things itself; restoring this snapshot whenever
// the child is confirmed gone or is about to be force-killed keeps that from
// becoming the user's problem.
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
	registerLiveAppChild(cmd, isTerminal, snap)

	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		deregisterLiveAppChild(cmd)
		snap.restore()
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
	_ = killAppChild(c.cmd, c.isTerminal)
	c.term.restore()
	select {
	case <-c.done:
	case <-time.After(appChildGracePeriod):
	}
}

type liveAppChild struct {
	cmd        *exec.Cmd
	isTerminal bool
	term       termSnapshot
}

var (
	liveAppChildrenMu sync.Mutex
	liveAppChildren   = map[*exec.Cmd]liveAppChild{}
)

func registerLiveAppChild(cmd *exec.Cmd, isTerminal bool, snap termSnapshot) {
	liveAppChildrenMu.Lock()
	liveAppChildren[cmd] = liveAppChild{cmd: cmd, isTerminal: isTerminal, term: snap}
	liveAppChildrenMu.Unlock()
}

func deregisterLiveAppChild(cmd *exec.Cmd) {
	liveAppChildrenMu.Lock()
	delete(liveAppChildren, cmd)
	liveAppChildrenMu.Unlock()
}

// killAllLiveAppChildren is the interrupt handler's forced-exit path (see
// exit.go's forceKillEverything): a best-effort kill fired synchronously,
// right before os.Exit, that cannot afford to wait on each child's own
// cmd.Wait() goroutine to get around to restoring the terminal. It restores
// eagerly here instead so a second Ctrl-C never trades a hung CLI for a
// glitched shell.
func killAllLiveAppChildren() {
	liveAppChildrenMu.Lock()
	children := make([]liveAppChild, 0, len(liveAppChildren))
	for _, c := range liveAppChildren {
		children = append(children, c)
	}
	liveAppChildrenMu.Unlock()

	for _, c := range children {
		_ = killAppChild(c.cmd, c.isTerminal)
		c.term.restore()
	}
}
