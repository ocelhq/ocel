package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
)

const appChildGracePeriod = 2 * time.Second

const appChildWaitDelay = appChildGracePeriod + 3*time.Second

type appChild struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  chan error
}

// spawnAppChild starts appCmd as the leader of its own process group,
// registers it so a force-kill (a second Ctrl-C, or the graceful window
// expiring) can reach it, and — when stdin is the controlling terminal —
// hands that group the terminal's foreground so a child that reads stdin
// or puts the terminal in raw mode is not stopped by SIGTTIN/SIGTTOU.
//
// Cancelling ctx sends SIGTERM to the group via appCmd.Cancel; if the group
// hasn't exited appChildGracePeriod later, it is SIGKILLed. WaitDelay is
// set as a backstop so an abandoned pipe cannot wedge Wait() indefinitely
// even if that escalation itself never runs.
func spawnAppChild(ctx context.Context, cmd *exec.Cmd, stdin io.Reader) (*appChild, error) {
	tty := ttyFile(stdin)
	setNewForegroundProcessGroup(cmd, tty)
	cmd.Cancel = func() error { return terminateProcessGroup(cmd) }
	cmd.WaitDelay = appChildWaitDelay

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	registerLiveAppChild(cmd)

	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		_ = restoreForegroundProcessGroup(tty)
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
			_ = killProcessGroup(cmd)
		}
	}()

	return &appChild{cmd: cmd, done: done, err: errCh}, nil
}

func (c *appChild) wait() error {
	return <-c.err
}

// stop kills the child's process group and waits, bounded, for it to be
// reaped. It is used by dev's follower loop to replace a running child on
// an env update or a leader disconnect — situations that are not the ctx
// cancellation spawnAppChild's own escalation goroutine watches for.
func (c *appChild) stop() {
	_ = killProcessGroup(c.cmd)
	select {
	case <-c.done:
	case <-time.After(appChildGracePeriod):
	}
}

func ttyFile(r io.Reader) *os.File {
	f, ok := r.(*os.File)
	if !ok {
		return nil
	}
	if !isatty.IsTerminal(f.Fd()) {
		return nil
	}
	return f
}

var (
	liveAppChildrenMu sync.Mutex
	liveAppChildren   = map[*exec.Cmd]struct{}{}
)

func registerLiveAppChild(cmd *exec.Cmd) {
	liveAppChildrenMu.Lock()
	liveAppChildren[cmd] = struct{}{}
	liveAppChildrenMu.Unlock()
}

func deregisterLiveAppChild(cmd *exec.Cmd) {
	liveAppChildrenMu.Lock()
	delete(liveAppChildren, cmd)
	liveAppChildrenMu.Unlock()
}

// killAllLiveAppChildren force-kills the process group of every dev/run app
// child that has been spawned and not yet reaped. It mirrors
// providerrunner.KillAllLive and exists for the same reason: a caller with
// no time left to wait on spawnAppChild's own grace period, e.g. a second
// Ctrl-C landing mid-shutdown.
func killAllLiveAppChildren() {
	liveAppChildrenMu.Lock()
	cmds := make([]*exec.Cmd, 0, len(liveAppChildren))
	for cmd := range liveAppChildren {
		cmds = append(cmds, cmd)
	}
	liveAppChildrenMu.Unlock()

	for _, cmd := range cmds {
		_ = killProcessGroup(cmd)
	}
}
