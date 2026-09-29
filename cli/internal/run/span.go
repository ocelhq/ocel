package run

import (
	"context"
	"crypto/rand"
	"io"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Span struct {
	run     *Run
	parent  *Span
	phase   progressv1.Phase
	subject string
	spanID  []byte
	start   time.Time
	title   progress.Title
	level   progressv1.Level

	mu         sync.Mutex
	writers    []*lineWriter
	attributes []*progressv1.SpanAttribute
	endOnce    sync.Once
}

func (s *Span) Unit(subject string, title progress.Title) *Span {
	return s.run.begin(s, subject, title)
}

func (s *Span) Trace(subject, name string, attrs ...progress.Attr) *Span {
	return s.run.beginTrace(s, subject, name, attrs)
}

func (s *Span) SetAttributes(attrs ...progress.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range attrs {
		s.attributes = append(s.attributes, &progressv1.SpanAttribute{Key: a.Key.Wire, Value: a.Value})
	}
}

type ReservedUnit struct {
	parent  *Span
	subject string
	title   progress.Title
	start   time.Time
}

func (s *Span) ReserveUnit(subject string, title progress.Title) ReservedUnit {
	return ReservedUnit{parent: s, subject: subject, title: title, start: s.run.bus.now()}
}

func (u ReservedUnit) Open() *Span {
	return u.parent.run.beginAt(u.parent, u.subject, u.title, u.start)
}

func (s *Span) Say(message string) { s.say(progressv1.Level_LEVEL_INFO, message) }

func (s *Span) Warn(message string) { s.say(progressv1.Level_LEVEL_WARN, message) }

func (s *Span) Debug(message string) { s.say(progressv1.Level_LEVEL_DEBUG, message) }

func (s *Span) Error(message string) { s.say(progressv1.Level_LEVEL_ERROR, message) }

func (s *Span) say(level progressv1.Level, message string) {
	s.run.bus.send(s.onSpan(&streamv1.RunEvent{Level: level, Message: message}))
}

func (s *Span) Output(level progressv1.Level, stream progressv1.Stream) io.Writer {
	w := &lineWriter{emit: func(line string) {
		s.run.bus.send(s.onSpan(&streamv1.RunEvent{
			Level:   level,
			Message: line,
			Body:    &streamv1.RunEvent_Output{Output: &progressv1.Output{Stream: stream}},
		}))
	}}
	s.mu.Lock()
	s.writers = append(s.writers, w)
	s.mu.Unlock()
	return w
}

func (s *Span) Hold(waiting *streamv1.WaitingEvent) (resume func(reason string)) {
	return s.run.holdOn(s.onSpan, waiting)
}

func (s *Span) Ask(ask func() error) error {
	return askHolding(s.Hold, ask)
}

func (s *Span) Confirm(confirm func() (bool, error)) (bool, error) {
	var granted bool
	err := s.Ask(func() (err error) {
		granted, err = confirm()
		return err
	})
	return granted, err
}

func (s *Span) Run() *Run { return s.run }

func (s *Span) Phase() progressv1.Phase { return s.phase }

func (s *Span) Plan(headline string, plan *planv1.ChangePlan, notes ...*planv1.Note) *planv1.ChangePlan {
	drawn := proto.CloneOf(plan)
	drawn.Headline, drawn.Notes = headline, notes
	shown := s.run.bus.send(s.onSpan(&streamv1.RunEvent{Body: &streamv1.RunEvent_Plan{Plan: drawn}}))
	return proto.CloneOf(shown.GetPlan())
}

func (s *Span) Identity(identity *streamv1.IdentityEvent) {
	s.run.identify(identity)
	s.run.bus.send(s.onSpan(&streamv1.RunEvent{Body: &streamv1.RunEvent_Identity{Identity: identity}}))
}

func (s *Span) Forward(op *progressv1.OperationEvent) {
	ev := runEventOf(op)
	s.run.enter(ev.GetPhase())
	if result := ev.GetResult(); result != nil {
		s.run.record(result)
	}
	s.run.bus.send(ev)
}

func runEventOf(op *progressv1.OperationEvent) *streamv1.RunEvent {
	ev := &streamv1.RunEvent{}
	wire, err := proto.Marshal(op)
	if err == nil {
		err = proto.Unmarshal(wire, ev)
	}
	if err != nil {
		return &streamv1.RunEvent{Level: progressv1.Level_LEVEL_ERROR, Message: "the provider sent an event this CLI cannot read: " + err.Error()}
	}
	return ev
}

func (s *Span) End(err error) {
	s.endOnce.Do(func() {
		for _, child := range slices.Backward(s.run.children(s)) {
			child.End(err)
		}
		s.flush()
		s.mu.Lock()
		ended := &progressv1.Ended{
			Status:            progressv1.SpanStatus_SPAN_STATUS_OK,
			StartTimeUnixNano: s.start.UnixNano(),
			Title:             s.title.Ended,
			Attributes:        s.attributes,
		}
		s.mu.Unlock()
		ev := &streamv1.RunEvent{Level: s.level, Body: &streamv1.RunEvent_Ended{Ended: ended}}
		if err != nil {
			ended.Status, ended.Title = progressv1.SpanStatus_SPAN_STATUS_ERROR, ""
			ev.Message = err.Error()
			if s.level != progressv1.Level_LEVEL_DEBUG {
				ev.Level = s.run.failureLevel()
			}
		}
		s.run.close(s)
		s.run.bus.send(s.onSpan(ev))
	})
}

func (s *Span) flush() {
	s.mu.Lock()
	writers := s.writers
	s.mu.Unlock()
	for _, w := range writers {
		w.flush()
	}
}

func (s *Span) onSpan(ev *streamv1.RunEvent) *streamv1.RunEvent {
	ev.Phase, ev.Subject, ev.SpanId = s.phase, s.subject, s.spanID
	return ev
}

func newSpanID() []byte {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return id
}

type spanKey struct{}

func ContextWithSpan(ctx context.Context, s *Span) context.Context {
	return context.WithValue(ctx, spanKey{}, s)
}

func SpanFromContext(ctx context.Context) *Span {
	s, _ := ctx.Value(spanKey{}).(*Span)
	return s
}
