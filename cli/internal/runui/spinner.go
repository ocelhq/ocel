package runui

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/fatih/color"
)

const frameRate = 100 * time.Millisecond

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spinnerFrame(n int) string {
	return spinnerFrames[n%len(spinnerFrames)]
}

type Spinner struct {
	out       io.Writer
	msg       string
	colored   bool
	mu        sync.Mutex
	stop      chan struct{}
	done      chan struct{}
	stopFn    func()
	suspendFn func() func()
	stopped   bool
}

func StartSpinner(present Presentation, out io.Writer, msg string) *Spinner {
	if !present.TTY || TerminalIsOwned() {
		return &Spinner{}
	}
	s := &Spinner{out: out, msg: msg, colored: present.Color}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startLocked()
	return s
}

func (s *Spinner) startLocked() {
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.loop(s.stop, s.done)
}

func (s *Spinner) eraseLocked() {
	if s.stop == nil {
		return
	}
	close(s.stop)
	<-s.done
	s.stop, s.done = nil, nil
	fmt.Fprint(s.out, "\r\033[K")
}

func (s *Spinner) loop(stop, done chan struct{}) {
	defer close(done)
	glyph := color.New(color.FgCyan)
	if s.colored {
		glyph.EnableColor()
	} else {
		glyph.DisableColor()
	}
	t := time.NewTicker(frameRate)
	defer t.Stop()
	frame := 0
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			fmt.Fprintf(s.out, "\r\033[K%s %s", glyph.Sprint(spinnerFrame(frame)), s.msg)
			frame++
		}
	}
}

func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	if s.stopFn != nil {
		s.stopFn()
		return
	}
	s.eraseLocked()
}

func (s *Spinner) Suspend() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return func() {}
	}
	if s.suspendFn != nil {
		return s.suspendFn()
	}
	if s.stop == nil {
		return func() {}
	}
	s.eraseLocked()

	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.stopped || s.stop != nil {
			return
		}
		s.startLocked()
	}
}
