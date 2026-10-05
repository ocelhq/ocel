package terminal

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

const (
	syncStart = "\x1b[?2026h"
	syncEnd   = "\x1b[?2026l"
	eraseLine = "\r\x1b[K"
)

type LiveTranscript struct {
	w       io.Writer
	present Presentation
	grouped *Transcript
	commits *bytes.Buffer
	live    *statusLine

	mu      sync.Mutex
	now     func() time.Time
	width   int
	drawn   string
	held    bool
	midRow  bool
	closed  bool
	ticking bool
	pace    func(running bool)
	measure func() int

	stop    func()
	looping chan struct{}
}

type liveSources struct {
	now     func() time.Time
	ticks   <-chan time.Time
	pace    func(running bool)
	resized <-chan os.Signal
	width   func() int
	release func()
}

func NewLiveTranscript(w io.Writer, present Presentation) *LiveTranscript {
	ticker := time.NewTicker(frameRate)
	resized, stopResizes := resizeSignals()
	return newLiveTranscript(w, present, liveSources{
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

func newLiveTranscript(w io.Writer, present Presentation, sources liveSources) *LiveTranscript {
	commits := &bytes.Buffer{}
	grouped := newTranscript(commits, present, nil)
	grouped.verbatimText = keepColour
	if present.Color {
		grouped.verbatimText = func(text string) string { return mutedToolText(keepColour(text)) }
	}
	stop := make(chan struct{})
	s := &LiveTranscript{
		w:       w,
		present: present,
		grouped: grouped,
		commits: commits,
		live:    newStatusLine(sources.now, present),
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

func (s *LiveTranscript) loop(sources liveSources, stop <-chan struct{}) {
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

func (s *LiveTranscript) Receive(ev *streamv1.RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grouped.Receive(ev)
	s.live.observe(ev)
	switch {
	case ev.GetSummary() != nil:
		s.live = newStatusLine(s.now, s.present)
	case ev.GetWaiting() != nil:
		s.held = true
	case ev.GetResumed() != nil && !s.closed:
		s.held = false
	}
	s.draw()
}

func (s *LiveTranscript) draw() {
	rowsAbove := 0
	if width := s.measure(); width > 0 && width != s.width {
		rowsAbove = displayWidth(s.drawn) / width
		s.width = width
	}
	line := ""
	if !s.held && !s.midRow {
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
	case (flushed || line == "") && !s.midRow:
		frame.WriteString(eraseLine)
	}
	frame.Write(s.commits.Bytes())
	s.commits.Reset()
	if line != "" {
		if flushed && !s.midRow || rowsAbove == 0 {
			frame.WriteString(eraseLine)
		}
		frame.WriteString(line)
	}
	frame.WriteString(syncEnd)
	_, _ = s.w.Write(frame.Bytes())
	s.drawn = line
}

func (s *LiveTranscript) tickWhile(drawn bool) {
	if drawn != s.ticking {
		s.ticking = drawn
		s.pace(drawn)
	}
}

func (s *LiveTranscript) Close() error {
	s.stop()
	<-s.looping
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.grouped.Close()
	s.held, s.closed = true, true
	s.draw()
	return err
}

func (s *LiveTranscript) Above(stdout File) File {
	return aboveLive{live: s, stdout: stdout}
}

type aboveLive struct {
	live   *LiveTranscript
	stdout File
}

func (a aboveLive) Fd() uintptr { return a.stdout.Fd() }

func (a aboveLive) Write(p []byte) (int, error) {
	s := a.live
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drawn != "" {
		_, _ = io.WriteString(s.w, syncStart+eraseLine+syncEnd)
		s.drawn = ""
	}
	n, err := a.stdout.Write(p)
	if n > 0 {
		s.midRow = p[n-1] != '\n'
	}
	s.draw()
	return n, err
}
