package cli

import (
	"context"
	"errors"
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

func ExitCode(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, true
	}
	if errors.Is(err, context.Canceled) {
		return interruptExitCode, true
	}
	return 0, false
}

const shutdownSlack = 3 * time.Second

const gracefulShutdownWindow = max(providerrunner.DefaultGracePeriod+providerrunner.DefaultReapTimeout, appChildWaitDelay) + shutdownSlack

func installInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return interruptHandlerWithExit(parent, stderr, ch, gracefulShutdownWindow, forceKillEverything, os.Exit)
}

func forceKillEverything() {
	providerrunner.KillAllLive()
	killAllLiveAppChildren()
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

		for {
			select {
			case <-done:
				return
			case sig := <-ch:
				if sig != os.Interrupt {
					continue
				}
				fmt.Fprintln(stderr, "Interrupted again: exiting immediately, cloud resources may be mid-flight.")
			case <-timer.C:
				fmt.Fprintf(stderr, "Graceful shutdown did not finish in %s: exiting, cloud resources may be mid-flight.\n", window)
			}
			forceKill()
			exit(interruptExitCode)
			return
		}
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
