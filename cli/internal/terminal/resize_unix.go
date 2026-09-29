//go:build unix

package terminal

import (
	"os"
	"os/signal"
	"syscall"
)

func resizeSignals() (<-chan os.Signal, func()) {
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	return resized, func() { signal.Stop(resized) }
}
