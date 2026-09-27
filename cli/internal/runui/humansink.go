package runui

import (
	"fmt"
	"io"
	"sync"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

type HumanSink struct {
	w io.Writer

	mu   sync.Mutex
	proj *projector
	r    *Renderer
}

func NewHumanSink(w io.Writer, present Presentation) *HumanSink {
	s := newHumanSink(w, present)
	s.r.startTicking()
	return s
}

func newHumanSink(w io.Writer, present Presentation) *HumanSink {
	return &HumanSink{w: w, proj: newProjector(present), r: newRenderer(w, present)}
}

func (s *HumanSink) Receive(ev *streamv1.RunEvent) {
	ev = normalize(ev)
	s.mu.Lock()
	defer s.mu.Unlock()
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
	fmt.Fprintln(s.w)
	return err
}

func (s *HumanSink) Suspend() func() { return s.r.Suspend() }

func (s *HumanSink) Spin(message string) *Spinner {
	return &Spinner{stopFn: s.r.Spin(message), suspendFn: s.r.Suspend}
}

func (s *HumanSink) Restart(stageID []byte) { s.r.Restart(stageID) }
