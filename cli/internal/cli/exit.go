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

const shutdownSlack = 3 * time.Second

const gracefulShutdownWindow = providerrunner.DefaultGracePeriod + providerrunner.DefaultReapTimeout + shutdownSlack

func installInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return interruptHandlerWithExit(parent, stderr, ch, gracefulShutdownWindow, providerrunner.KillAllLive, os.Exit)
}

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
