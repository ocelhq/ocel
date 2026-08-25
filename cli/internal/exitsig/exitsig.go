package exitsig

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
)

type ExitError struct {
	Code int
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.Code)
}

const InterruptCode = 130

func ExitCode(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code, true
	}
	if errors.Is(err, context.Canceled) {
		return InterruptCode, true
	}
	return 0, false
}

func Install(parent context.Context, stderr io.Writer, window time.Duration, forceKill func()) (context.Context, context.CancelFunc) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return InstallWithExit(parent, stderr, ch, window, forceKill, os.Exit)
}

func InstallWithExit(parent context.Context, stderr io.Writer, ch chan os.Signal, window time.Duration, forceKill func(), exit func(int)) (context.Context, context.CancelFunc) {
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
			exit(InterruptCode)
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
