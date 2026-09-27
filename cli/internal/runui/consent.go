package runui

import (
	"context"
	"sync"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func (s *Session) Interactive() bool { return s.gate.Interactive }

func (s *Session) Dry() bool { return s.gate.Dry }

func (s *Session) Asking() bool { return s.gate.Asking() }

func (s *Session) Guard(ctx context.Context, question string) (bool, error) {
	return s.gate.Guard(ctx, sessionScope{s}, question)
}

func (s *Session) Consent(ctx context.Context, question string) (bool, error) {
	return s.gate.Consent(ctx, sessionScope{s}, s.shown, question)
}

func (s *Session) ConsentByName(ctx context.Context, label, name string) (bool, error) {
	return s.gate.ConsentByName(ctx, sessionScope{s}, s.shown, label, name)
}

type sessionScope struct{ *Session }

func (h sessionScope) Say(message string) { h.Diagnostic(message) }

func (h sessionScope) Hold(waiting *streamv1.WaitingEvent) func(reason string) {
	h.waiting = true
	h.emit(&streamv1.RunEvent{Body: &streamv1.RunEvent_Waiting{Waiting: waiting}})
	var once sync.Once
	return func(reason string) {
		once.Do(func() {
			h.waiting = false
			h.emit(&streamv1.RunEvent{Body: &streamv1.RunEvent_Resumed{Resumed: &streamv1.ResumedEvent{Reason: reason}}})
		})
	}
}
