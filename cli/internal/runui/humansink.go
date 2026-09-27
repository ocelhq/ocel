package runui

import (
	"fmt"
	"io"
	"sync"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

type HumanSink struct {
	w io.Writer

	mu       sync.Mutex
	proj     *projector
	r        *Renderer
	received bool
}

func NewHumanSink(w io.Writer, present Presentation) *HumanSink {
	s := newHumanSink(w, present)
	s.r.ticks = true
	return s
}

func newHumanSink(w io.Writer, present Presentation) *HumanSink {
	return &HumanSink{w: w, proj: newProjector(present), r: newRenderer(w, present)}
}

func (s *HumanSink) Receive(ev *streamv1.RunEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = true
	lines := s.proj.project(ev)
	if ev.GetWaiting() != nil {
		s.r.Pause()
	}
	s.r.Ingest(ev)
	s.r.Commit(lines)
	if ev.GetResumed() != nil {
		s.r.Resume()
	}
}

func (s *HumanSink) Close() error {
	err := s.r.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.received {
		fmt.Fprintln(s.w)
	}
	return err
}
