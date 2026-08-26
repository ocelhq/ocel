package providerkit

import (
	"context"
	"errors"
	"sync"
	"time"

	connect "connectrpc.com/connect"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const eventSenderBuffer = 256

type eventSender struct {
	events chan *progressv1.OperationEvent
	done   chan struct{}
	ctx    context.Context

	mu     sync.RWMutex
	closed bool
	err    error
}

func newEventSender(ctx context.Context, send func(*progressv1.OperationEvent) error) *eventSender {
	s := &eventSender{
		events: make(chan *progressv1.OperationEvent, eventSenderBuffer),
		done:   make(chan struct{}),
		ctx:    ctx,
	}
	go s.drain(send)
	return s
}

func (s *eventSender) drain(send func(*progressv1.OperationEvent) error) {
	defer close(s.done)
	for ev := range s.events {
		if err := send(ev); err != nil && s.err == nil {
			s.err = err
		}
	}
}

func (s *eventSender) send(ev *progressv1.OperationEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.events <- ev:
	case <-s.ctx.Done():
	}
}

func (s *eventSender) fail(err error) error {
	if refusedRequest(err) {
		return err
	}
	s.send(failureResult(err))
	return nil
}

func (s *eventSender) close() error {
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
	do func(*eventSender) (*progressv1.OperationEvent, error),
) (err error) {
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()

	result, err := do(sender)
	if err != nil {
		return sender.fail(RefusalError(err))
	}
	sender.send(result)
	return nil
}

func streamed(
	ctx context.Context,
	stream *connect.ServerStream[progressv1.OperationEvent],
	unit, title string,
	phase progressv1.Phase,
	do func(*eventSender, Reporter) error,
) error {
	return streamResult(ctx, stream, func(sender *eventSender) (*progressv1.OperationEvent, error) {
		root := UnitStage(unit, title)
		working := PhaseStage(unit, phase)
		newEventTracer(sender).DeclareStages(root, working)
		if err := do(sender, newReporter(sender, working)); err != nil {
			return nil, err
		}
		return okResult(), nil
	})
}

type reporter struct {
	sender *eventSender
	tracer *eventTracer
	stage  Stage
}

func newReporter(sender *eventSender, stage Stage) Reporter {
	return &reporter{sender: sender, tracer: newEventTracer(sender), stage: stage}
}

func (r *reporter) Say(message string) {
	r.sender.send(stageProgressEvent(r.stage.ID, sanitizeMessage(message)))
}

func (r *reporter) Detail(message string) {
	r.sender.send(logEvent(r.stage.ID, sanitizeMessage(message)))
}

func (r *reporter) Span(name string, start, end time.Time, err error, attrs ...Attr) {
	r.tracer.Span(newStageID(), r.stage.ID, sanitizeTitle(name), start, end, err, attrs...)
}

func stageProgressEvent(id StageID, message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Progress{Progress: &progressv1.ProgressEvent{
			Message: message,
			StageId: id[:],
		}},
	}
}

func degradedEvent(need edge.Need, detail string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Degraded{Degraded: &progressv1.DegradedEvent{
			Need:   string(need),
			Detail: detail,
		}},
	}
}

func dnsOwedEvent(headline string, records []edge.Record, notes ...string) *progressv1.OperationEvent {
	owed := make([]*progressv1.DnsRecord, 0, len(records))
	for _, rec := range records {
		owed = append(owed, &progressv1.DnsRecord{
			Name:    rec.Name,
			Type:    string(rec.Type),
			Value:   rec.Value,
			Proxied: rec.Proxied,
		})
	}
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_DnsOwed{DnsOwed: &progressv1.DnsOwedEvent{
			Headline: headline,
			Records:  owed,
			Notes:    notes,
		}},
	}
}

func logEvent(id StageID, message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Log{Log: &progressv1.LogEvent{Message: message, StageId: id[:]}},
	}
}

func refusedRequest(err error) bool {
	return connect.CodeOf(err) == connect.CodeInvalidArgument
}

func failStream(stream *connect.ServerStream[progressv1.OperationEvent], err error) error {
	if refusedRequest(err) {
		return err
	}
	return stream.Send(failureResult(err))
}

func failureResult(err error) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{
			Success: false,
			Error:   err.Error(),
		}},
	}
}

func okResult() *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{Success: true}},
	}
}
