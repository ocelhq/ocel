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
	stop    chan struct{}
	done    chan struct{}
	stopFn  func()
	once    sync.Once
}

func StartSpinner(present Presentation, out io.Writer, msg string) *Spinner {
	if !present.TTY || TerminalIsOwned() {
		return &Spinner{}
	}
	s := &Spinner{out: out, msg: msg, colored: present.Color}
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	go s.loop()
	return s
}

func (s *Spinner) loop() {
	defer close(s.done)
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
		if s.stopFn != nil {
			s.stopFn()
			return
		}
		if s.stop == nil {
			return
		}
		close(s.stop)
		<-s.done
		fmt.Fprint(s.out, "\r\033[K")
	})
}
