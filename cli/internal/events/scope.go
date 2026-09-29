package events

import (
	"crypto/rand"
	"io"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Scope struct {
	run     *Run
	parent  *Scope
	phase   progressv1.Phase
	subject string
	spanID  []byte
	start   time.Time
	title   string

	mu      sync.Mutex
	writers []*lineWriter
	endOnce sync.Once
}

func (s *Scope) Unit(subject, message string) *Scope {
	return s.run.begin(s, subject, message)
}

type ReservedUnit struct {
	parent           *Scope
	subject, message string
	start            time.Time
}

func (s *Scope) ReserveUnit(subject, message string) ReservedUnit {
	return ReservedUnit{parent: s, subject: subject, message: message, start: s.run.bus.now()}
}

func (u ReservedUnit) Open() *Scope {
	return u.parent.run.beginAt(u.parent, u.subject, u.message, u.start)
}

func (s *Scope) Say(message string) { s.say(progressv1.Level_LEVEL_INFO, message) }

func (s *Scope) Warn(message string) { s.say(progressv1.Level_LEVEL_WARN, message) }

func (s *Scope) Debug(message string) { s.say(progressv1.Level_LEVEL_DEBUG, message) }

func (s *Scope) Error(message string) { s.say(progressv1.Level_LEVEL_ERROR, message) }

func (s *Scope) say(level progressv1.Level, message string) {
	s.run.bus.send(s.scoped(&streamv1.RunEvent{Level: level, Message: message}))
}

func (s *Scope) Output(level progressv1.Level, stream progressv1.Stream) io.Writer {
	w := &lineWriter{emit: func(line string) {
		s.run.bus.send(s.scoped(&streamv1.RunEvent{
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

func (s *Scope) Hold(waiting *streamv1.WaitingEvent) (resume func(reason string)) {
	return s.run.holdOn(s.scoped, waiting)
}

func (s *Scope) Run() *Run { return s.run }

func (s *Scope) Phase() progressv1.Phase { return s.phase }

func (s *Scope) Plan(headline string, plan *planv1.ChangePlan, notes ...*planv1.Note) *planv1.ChangePlan {
	drawn := proto.CloneOf(plan)
	drawn.Headline, drawn.Notes = headline, notes
	shown := s.run.bus.send(s.scoped(&streamv1.RunEvent{Body: &streamv1.RunEvent_Plan{Plan: drawn}}))
	return proto.CloneOf(shown.GetPlan())
}

func (s *Scope) Identity(identity *streamv1.IdentityEvent) {
	s.run.bus.send(s.scoped(&streamv1.RunEvent{Body: &streamv1.RunEvent_Identity{Identity: identity}}))
}

func (s *Scope) Forward(op *progressv1.OperationEvent) {
	ev := lift(op)
	s.run.enter(ev.GetPhase())
	if apps := ev.GetOutcome().GetApps(); len(apps) > 0 {
		s.run.record(apps)
	}
	s.run.bus.send(ev)
}

func lift(op *progressv1.OperationEvent) *streamv1.RunEvent {
	ev := &streamv1.RunEvent{
		Level:   op.GetLevel(),
		Phase:   op.GetPhase(),
		Subject: op.GetSubject(),
		Message: op.GetMessage(),
		SpanId:  op.GetSpanId(),
	}
	if ns := op.GetTimeUnixNano(); ns > 0 {
		ev.Time = timestamppb.New(time.Unix(0, ns))
	}
	switch body := op.GetBody().(type) {
	case *progressv1.OperationEvent_Started:
		ev.Body = &streamv1.RunEvent_Started{Started: body.Started}
	case *progressv1.OperationEvent_Ended:
		ev.Body = &streamv1.RunEvent_Ended{Ended: body.Ended}
	case *progressv1.OperationEvent_Output:
		ev.Body = &streamv1.RunEvent_Output{Output: body.Output}
	case *progressv1.OperationEvent_Plan:
		ev.Body = &streamv1.RunEvent_Plan{Plan: body.Plan}
	case *progressv1.OperationEvent_DnsManualRecords:
		ev.Body = &streamv1.RunEvent_DnsManualRecords{DnsManualRecords: body.DnsManualRecords}
	case *progressv1.OperationEvent_Result:
		ev.Body = &streamv1.RunEvent_Outcome{Outcome: body.Result}
	}
	return ev
}

func (s *Scope) End(err error) {
	s.endOnce.Do(func() {
		for _, child := range slices.Backward(s.run.children(s)) {
			child.End(err)
		}
		s.flush()
		ended := &progressv1.Ended{
			Status:            progressv1.SpanStatus_SPAN_STATUS_OK,
			StartTimeUnixNano: s.start.UnixNano(),
			Title:             s.title,
		}
		ev := &streamv1.RunEvent{Level: progressv1.Level_LEVEL_INFO, Body: &streamv1.RunEvent_Ended{Ended: ended}}
		if err != nil {
			ended.Status, ended.Title = progressv1.SpanStatus_SPAN_STATUS_ERROR, ""
			ev.Level, ev.Message = s.run.failureLevel(), err.Error()
		}
		s.run.close(s)
		s.run.bus.send(s.scoped(ev))
	})
}

func (s *Scope) flush() {
	s.mu.Lock()
	writers := s.writers
	s.mu.Unlock()
	for _, w := range writers {
		w.flush()
	}
}

func (s *Scope) scoped(ev *streamv1.RunEvent) *streamv1.RunEvent {
	ev.Phase, ev.Subject, ev.SpanId = s.phase, s.subject, s.spanID
	return ev
}

func newSpanID() []byte {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return id
}
