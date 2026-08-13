package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/cli/internal/providerrunner"
)

type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

const interruptExitCode = 130

// shutdownSlack covers what still has to happen after Runner.Close()
// returns: ui.Close(), the .ocel result write, and the RPC unwind back up
// to main. It is not itself teardown time, so it is not part of the
// provider's grace+reap budget.
const shutdownSlack = 3 * time.Second

// gracefulShutdownWindow is derived from the provider teardown budget
// (grace period to let it exit on its own, plus the bounded final reap)
// rather than chosen independently of it: a window shorter than that
// budget would force-exit on top of a teardown that was still running
// normally.
const gracefulShutdownWindow = providerrunner.DefaultGracePeriod + providerrunner.DefaultReapTimeout + shutdownSlack

func installInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return interruptHandlerWithExit(parent, stderr, ch, gracefulShutdownWindow, providerrunner.KillAllLive, os.Exit)
}

// interruptHandlerWithExit is a second interrupt (or the graceful window
// expiring) away from calling exit() with no further cleanup: whatever a
// normal Close() would have done is not going to happen. forceKill is the
// one piece of that cleanup worth doing anyway — a best-effort process-group
// kill of any provider still running — because without it a live provider's
// pulumi grandchild is orphaned the instant an impatient second Ctrl-C lands
// mid-grace-period, which is exactly when a slow teardown makes the CLI look
// hung and invites one.
func interruptHandlerWithExit(parent context.Context, stderr io.Writer, ch chan os.Signal, window time.Duration, forceKill func(), exit func(int)) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})

	go func() {
		select {
		case <-done:
			return
		case <-ch:
		}
		cancel()

		timer := time.NewTimer(window)
		defer timer.Stop()

		select {
		case <-done:
			return
		case <-ch:
			fmt.Fprintln(stderr, "Interrupted again: exiting immediately, cloud resources may be mid-flight.")
		case <-timer.C:
			fmt.Fprintf(stderr, "Graceful shutdown did not finish in %s: exiting, cloud resources may be mid-flight.\n", window)
		}
		forceKill()
		exit(interruptExitCode)
	}()

	var stopOnce sync.Once
	return ctx, func() {
		stopOnce.Do(func() {
			signal.Stop(ch)
			close(done)
			cancel()
		})
	}
}
