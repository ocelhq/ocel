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
	measure func() int

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
		measure: sources.width,
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
		case <-sources.resized:
		}
		s.mu.Lock()
		s.draw()
		s.mu.Unlock()
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

func (s *LineSink) paintGlyph(line string) string {
	_, size := utf8.DecodeRuneInString(line)
	return colorFor(s.present, color.FgCyan).Sprint(line[:size]) + line[size:]
}

func (s *LineSink) draw() {
	rowsAbove := 0
	if width := s.measure(); width > 0 && width != s.width {
		rowsAbove = displayWidth(s.drawn) / width
		s.width = width
	}
	line := ""
	if !s.held {
		line = s.live.render(s.width)
	}
	s.tickWhile(line != "")
	flushed := s.commits.Len() > 0
	if rowsAbove == 0 && !flushed && line == s.drawn {
		return
	}
	var frame bytes.Buffer
	frame.WriteString(syncStart)
	switch {
	case rowsAbove > 0:
		fmt.Fprintf(&frame, "\x1b[%dA\r\x1b[J", rowsAbove)
	case flushed || line == "":
		frame.WriteString(eraseLine)
	}
	frame.Write(s.commits.Bytes())
	s.commits.Reset()
	if line != "" {
		if flushed || rowsAbove == 0 {
			frame.WriteString(eraseLine)
		}
		frame.WriteString(s.paintGlyph(line))
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
