package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

const interruptExitCode = 130

const gracefulShutdownWindow = 10 * time.Second

func installInterruptHandler(parent context.Context, stderr io.Writer) (context.Context, context.CancelFunc) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return interruptHandlerWithExit(parent, stderr, ch, gracefulShutdownWindow, os.Exit)
}

func interruptHandlerWithExit(parent context.Context, stderr io.Writer, ch chan os.Signal, window time.Duration, exit func(int)) (context.Context, context.CancelFunc) {
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
		case <-timer.C:
		}
		fmt.Fprintln(stderr, "Interrupted again: exiting immediately, cloud resources may be mid-flight.")
		exit(interruptExitCode)
	}()

	return ctx, func() {
		signal.Stop(ch)
		close(done)
		cancel()
	}
}
