package interrupt

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/ocelhq/ocel/cli/internal/exitcode"
)

func Handle(parent context.Context, stderr io.Writer, window time.Duration, teardown, forceKill func()) (context.Context, context.CancelFunc) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return handle(parent, stderr, ch, window, teardown, forceKill, os.Exit)
}

func handle(parent context.Context, stderr io.Writer, ch chan os.Signal, window time.Duration, teardown, forceKill func(), exit func(int)) (context.Context, context.CancelFunc) {
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

		var notice string
		for notice == "" {
			select {
			case <-done:
				return
			case sig := <-ch:
				if sig != os.Interrupt {
					continue
				}
				notice = "Interrupted again: exiting immediately, cloud resources may be mid-flight."
			case <-timer.C:
				notice = fmt.Sprintf("Graceful shutdown did not finish in %s: exiting, cloud resources may be mid-flight.", window)
			}
		}
		teardown()
		fmt.Fprintln(stderr, notice)
		forceKill()
		exit(exitcode.Interrupt)
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
