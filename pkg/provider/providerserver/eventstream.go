package providerserver

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	connect "connectrpc.com/connect"

	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const eventSenderBuffer = 256

type eventStream struct {
	events chan *progressv1.OperationEvent
	done   chan struct{}
	ctx    context.Context

	detail  func(*progressv1.ResultEvent)
	refuses []connect.Code

	mu     sync.RWMutex
	closed bool
	err    error
}

func newEventStream(ctx context.Context, send func(*progressv1.OperationEvent) error) *eventStream {
	s := &eventStream{
		events: make(chan *progressv1.OperationEvent, eventSenderBuffer),
		done:   make(chan struct{}),
		ctx:    ctx,
	}
	go s.drain(send)
	return s
}

func (s *eventStream) refusing(code connect.Code) {
	s.refuses = append(s.refuses, code)
}

func (s *eventStream) detailing(detail func(*progressv1.ResultEvent)) {
	s.detail = detail
}

func (s *eventStream) drain(send func(*progressv1.OperationEvent) error) {
	defer close(s.done)
	for ev := range s.events {
		if err := send(ev); err != nil && s.err == nil {
			s.err = err
		}
	}
}

func (s *eventStream) send(ev *progressv1.OperationEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	if ev.GetTimeUnixNano() == 0 {
		ev.TimeUnixNano = time.Now().UnixNano()
	}
	if ev.GetLevel() == progressv1.Level_LEVEL_UNSPECIFIED {
		ev.Level = progressv1.Level_LEVEL_INFO
	}
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *eventStream) fail(err error) error {
	event := failureResult(err)
	if s.detail != nil {
		s.detail(event.GetResult())
	}
	refused := refusedRequest(err) || slices.Contains(s.refuses, connect.CodeOf(err))
	event.GetResult().Refused = refused
	s.send(event)
	if refused {
		return err
	}
	return nil
}

func (s *eventStream) close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	close(s.events)
	<-s.done
	return s.err
}

func streamResult(
	ctx context.Context,
	stream *connect.ServerStream[progressv1.OperationEvent],
	do func(*eventStream) (*progressv1.OperationEvent, error),
) (err error) {
	sender := newEventStream(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()

	result, err := do(sender)
	if err != nil {
		return sender.fail(provider.RefusalError(err))
	}
	sender.send(result)
	return nil
}

func streamed(
	ctx context.Context,
	stream *connect.ServerStream[progressv1.OperationEvent],
	unit, title string,
	phase progressv1.Phase,
	do func(*eventStream, edge.Progress) error,
) error {
	return streamResult(ctx, stream, func(sender *eventStream) (*progressv1.OperationEvent, error) {
		if err := inUnit(sender, unit, title, phase, do); err != nil {
			return nil, err
		}
		return okResult(), nil
	})
}

func inUnit(
	sender *eventStream,
	unit, title string,
	phase progressv1.Phase,
	do func(*eventStream, edge.Progress) error,
) error {
	return newStageScope(sender).unit(UnitStage(unit, title, phase), func(u *unitRun) error {
		return u.phase(func(progress edge.Progress) error {
			return do(sender, progress)
		})
	})
}

func planEvent(plan *planv1.ChangePlan) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{Body: &progressv1.OperationEvent_Plan{Plan: plan}}
}

type stageProgress struct {
	sender *eventStream
	trace  *eventTrace
	stage  Stage
}

func newProgress(sender *eventStream, stage Stage) edge.Progress {
	return &stageProgress{sender: sender, trace: newEventTrace(sender), stage: stage}
}

func (r *stageProgress) Say(message string) {
	r.sender.send(r.stage.scoped(&progressv1.OperationEvent{Message: sanitizeMessage(message)}))
}

func (r *stageProgress) Warn(message string) {
	r.sender.send(r.stage.scoped(&progressv1.OperationEvent{
		Level:   progressv1.Level_LEVEL_WARN,
		Message: sanitizeMessage(message),
	}))
}

func (r *stageProgress) Detail(message string) {
	r.sender.send(r.stage.scoped(outputEvent(progressv1.Level_LEVEL_INFO, sanitizeMessage(message))))
}

func (r *stageProgress) Debug(line string) {
	r.sender.send(r.stage.scoped(outputEvent(progressv1.Level_LEVEL_DEBUG, sanitizeMessage(line))))
}

func (r *stageProgress) Span(name string, start, end time.Time, err error, attrs ...edge.Attr) {
	detail := NewStage(r.stage, name)
	r.trace.Start(start, detail)
	r.trace.End(detail, start, end, err, attrs...)
}

func degradedEvent(app string, need edge.Need, detail string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level:   progressv1.Level_LEVEL_WARN,
		Phase:   progressv1.Phase_PHASE_CHECK,
		Subject: app,
		Message: string(need) + ": " + detail,
	}
}

func checkWarning(subject, message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level:   progressv1.Level_LEVEL_WARN,
		Phase:   progressv1.Phase_PHASE_CHECK,
		Subject: subject,
		Message: message,
	}
}

func dnsManualRecordsEvent(headline string, records []edge.Record, notes ...string) *progressv1.OperationEvent {
	manual := make([]*progressv1.DnsRecord, 0, len(records))
	for _, rec := range records {
		manual = append(manual, &progressv1.DnsRecord{
			Name:    rec.Name,
			Type:    string(rec.Type),
			Value:   rec.Value,
			Proxied: rec.Proxied,
		})
	}
	return &progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_DnsManualRecords{DnsManualRecords: &progressv1.DnsManualRecordsEvent{
			Headline: headline,
			Records:  manual,
			Notes:    notes,
		}},
	}
}

func outputEvent(level progressv1.Level, line string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level:   level,
		Message: line,
		Body:    &progressv1.OperationEvent_Output{Output: &progressv1.Output{}},
	}
}

func refusedRequest(err error) bool {
	return connect.CodeOf(err) == connect.CodeInvalidArgument
}

func failureResult(err error) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Level: progressv1.Level_LEVEL_ERROR,
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{
			Success: false,
			Error:   err.Error(),
		}},
	}
}

func okResult() *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{Success: true}},
	}
}
