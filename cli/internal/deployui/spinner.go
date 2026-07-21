package deployui

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/fatih/color"
)

// Spinner is a standalone one-line spinner for a discrete wait that isn't a
// deploy phase — e.g. the preflight credential check, which runs before any
// Session step is active. It animates only when out is a terminal; on a
// non-terminal (CI, a captured buffer) it is inert so nothing corrupts the
// logs. Stop clears the line, so the caller can print its result as though the
// spinner never showed.
type Spinner struct {
	out  io.Writer
	msg  string
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// StartSpinner begins animating "<glyph> msg" on out, repainting in place until
// Stop. On a non-terminal out it returns an inert spinner (no goroutine, no
// output). msg is fixed for the spinner's life.
func StartSpinner(out io.Writer, msg string) *Spinner {
	s := &Spinner{out: out, msg: msg}
	if !isTTY(out) {
		return s
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.loop()
	return s
}

func (s *Spinner) loop() {
	defer close(s.done)
	t := time.NewTicker(frameRate)
	defer t.Stop()
	frame := 0
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			glyph := color.New(color.FgCyan).Sprint(spinnerFrames[frame%len(spinnerFrames)])
			fmt.Fprintf(s.out, "\r\033[K%s %s", glyph, s.msg)
			frame++
		}
	}
}

// Stop halts the animation and clears the spinner's line. It is safe to call on
// an inert spinner and safe to call more than once.
func (s *Spinner) Stop() {
	s.once.Do(func() {
		if s.stop == nil {
			return
		}
		close(s.stop)
		<-s.done
		fmt.Fprint(s.out, "\r\033[K")
	})
}
