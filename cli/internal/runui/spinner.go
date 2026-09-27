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
	out     io.Writer
	msg     string
	colored bool
	mu      sync.Mutex
	stop    chan struct{}
	done    chan struct{}
	stopped bool
	frame   int
}

func StartSpinner(present Presentation, out io.Writer, msg string) *Spinner {
	if !present.TTY {
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
	t := time.NewTicker(frameRate)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.tick()
		}
	}
}

func (s *Spinner) tick() {
	if s.out == nil {
		return
	}
	glyph := color.New(color.FgCyan)
	if s.colored {
		glyph.EnableColor()
	} else {
		glyph.DisableColor()
	}
	fmt.Fprintf(s.out, "\r\033[K%s %s", glyph.Sprint(spinnerFrame(s.frame)), s.msg)
	s.frame++
}

func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	s.eraseLocked()
}
