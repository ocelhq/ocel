package runui

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fatih/color"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

const (
	syncStart = "\x1b[?2026h"
	syncEnd   = "\x1b[?2026l"
	eraseLine = "\r\x1b[K"
)

type LineSink struct {
	w       io.Writer
	present Presentation
	grouped *GroupedSink
	commits *bytes.Buffer
	live    *liveLine

	mu      sync.Mutex
	now     func() time.Time
	width   int
	drawn   string
	held    bool
	closed  bool
	ticking bool
	pace    func(running bool)

	stop    func()
	looping chan struct{}
}

type lineSources struct {
	now     func() time.Time
	ticks   <-chan time.Time
	pace    func(running bool)
	resized <-chan os.Signal
	width   func() int
	release func()
}

func NewLineSink(w io.Writer, present Presentation) *LineSink {
	ticker := time.NewTicker(frameRate)
	resized, stopResizes := resizeSignals()
	return newLineSink(w, present, lineSources{
		now:   time.Now,
		ticks: ticker.C,
		pace: func(running bool) {
			if running {
				ticker.Reset(frameRate)
				return
			}
			ticker.Stop()
		},
		resized: resized,
		width: func() int {
			width, _ := liveWidth(w)
			return width
		},
		release: func() {
			ticker.Stop()
			stopResizes()
		},
	})
}

func newLineSink(w io.Writer, present Presentation, sources lineSources) *LineSink {
	commits := &bytes.Buffer{}
	stop := make(chan struct{})
	s := &LineSink{
		w:       w,
		present: present,
		grouped: newGroupedSink(commits, present, nil),
		commits: commits,
		live:    newLiveLine(sources.now),
		now:     sources.now,
		width:   present.Width,
		ticking: true,
		pace:    sources.pace,
		stop: sync.OnceFunc(func() {
			if sources.release != nil {
				sources.release()
			}
			close(stop)
		}),
		looping: make(chan struct{}),
	}
	if s.pace == nil {
		s.pace = func(bool) {}
	}
	go s.loop(sources, stop)
	return s
}

func (s *LineSink) loop(sources lineSources, stop <-chan struct{}) {
	defer close(s.looping)
	for {
		select {
		case <-stop:
			return
		case <-sources.ticks:
			s.mu.Lock()
			s.draw()
			s.mu.Unlock()
		case <-sources.resized:
			s.resize(sources.width())
		}
	}
}

func (s *LineSink) Receive(ev *streamv1.RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grouped.Receive(ev)
	s.live.observe(ev)
	switch {
	case ev.GetResult() != nil:
		s.live = newLiveLine(s.now)
	case ev.GetWaiting() != nil:
		s.held = true
	case ev.GetResumed() != nil && !s.closed:
		s.held = false
	}
	s.draw()
}

func (s *LineSink) resize(width int) {
	if width <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.width = width
	rowsAbove := displayWidth(s.drawn) / width
	if rowsAbove == 0 {
		s.draw()
		return
	}
	s.drawn = s.live.render(width)
	fmt.Fprintf(s.w, "%s\x1b[%dA\r\x1b[J%s%s", syncStart, rowsAbove, s.painted(s.drawn), syncEnd)
}

func (s *LineSink) painted(line string) string {
	_, size := utf8.DecodeRuneInString(line)
	return colorFor(s.present, color.FgCyan).Sprint(line[:size]) + line[size:]
}

func (s *LineSink) draw() {
	line := ""
	if !s.held {
		line = s.live.render(s.width)
	}
	s.tickWhile(line != "")
	if s.commits.Len() == 0 && line == s.drawn {
		return
	}
	var frame bytes.Buffer
	frame.WriteString(syncStart)
	if s.commits.Len() > 0 || line == "" {
		frame.WriteString(eraseLine)
		frame.Write(s.commits.Bytes())
		s.commits.Reset()
	}
	if line != "" {
		frame.WriteString(eraseLine + s.painted(line))
	}
	frame.WriteString(syncEnd)
	_, _ = s.w.Write(frame.Bytes())
	s.drawn = line
}

func (s *LineSink) tickWhile(drawn bool) {
	if drawn != s.ticking {
		s.ticking = drawn
		s.pace(drawn)
	}
}

func (s *LineSink) Close() error {
	s.stop()
	<-s.looping
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.grouped.Close()
	s.held, s.closed = true, true
	s.draw()
	return err
}
