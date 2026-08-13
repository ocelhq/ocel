package deployui

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/fatih/color"
)

const frameRate = 100 * time.Millisecond

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerFrame is the one place that maps a frame counter to a glyph. The
// Renderer's live region and the standalone Spinner both call it, so there
// is exactly one spinner implementation.
func spinnerFrame(n int) string {
	return spinnerFrames[n%len(spinnerFrames)]
}

// Spinner is a single-line, single-message animation for the ad-hoc waits
// that happen before a Session exists (checking credentials, listing
// projects) and so have no stage or live region to belong to.
type Spinner struct {
	out  io.Writer
	msg  string
	stop chan struct{}
	done chan struct{}
	once sync.Once
}

func StartSpinner(out io.Writer, msg string) *Spinner {
	return startSpinner(out, msg, IsTerminal(out))
}

func startSpinner(out io.Writer, msg string, animate bool) *Spinner {
	s := &Spinner{out: out, msg: msg}
	if !animate {
		return s
	}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.loop()
	return s
}

func (s *Spinner) loop() {
	defer close(s.done)
	glyph := color.New(color.FgCyan)
	if !IsTerminal(s.out) {
		glyph.DisableColor()
	}
	t := time.NewTicker(frameRate)
	defer t.Stop()
	frame := 0
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			fmt.Fprintf(s.out, "\r\033[K%s %s", glyph.Sprint(spinnerFrame(frame)), s.msg)
			frame++
		}
	}
}

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
