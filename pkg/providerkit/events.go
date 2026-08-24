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
	phase progressv1.Phase,
	do func(*eventSender, Reporter) (*progressv1.OperationEvent, error),
) (err error) {
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()

	result, err := do(sender, newReporter(sender, Stage{}, phase))
	if err != nil {
		return sender.fail(RefusalError(err))
	}
	sender.send(result)
	return nil
}

func streamed(
	ctx context.Context,
	stream *connect.ServerStream[progressv1.OperationEvent],
	phase progressv1.Phase,
	do func(*eventSender, Reporter) error,
) error {
	return streamResult(ctx, stream, phase, func(sender *eventSender, report Reporter) (*progressv1.OperationEvent, error) {
		if err := do(sender, report); err != nil {
			return nil, err
		}
		return okResult(), nil
	})
}

type reporter struct {
	sender *eventSender
	tracer *eventTracer
	stage  Stage
	phase  progressv1.Phase
}

func newReporter(sender *eventSender, stage Stage, phase progressv1.Phase) Reporter {
	return &reporter{sender: sender, tracer: newEventTracer(sender), stage: stage, phase: phase}
}

func (r *reporter) Say(message string) {
	if r.stage.ID == (StageID{}) {
		r.sender.send(progressEvent(sanitizeMessage(message)))
		return
	}
	r.sender.send(stageProgressEvent(r.stage.ID, r.phase, sanitizeMessage(message)))
}

func (r *reporter) Detail(message string) {
	r.sender.send(logEvent(sanitizeMessage(message)))
}

func (r *reporter) Span(name string, start, end time.Time, err error, attrs ...Attr) {
	r.tracer.Span(newStageID(), r.stage.ID, sanitizeTitle(name), start, end, err, attrs...)
}

func progressEvent(message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Progress{Progress: &progressv1.ProgressEvent{Message: message}},
	}
}

func phaseProgressEvent(stageID []byte, phase progressv1.Phase, message string, current, total uint32) *progressv1.OperationEvent {
	p := &progressv1.ProgressEvent{Message: message, Phase: phase, StageId: stageID}
	if total > 0 {
		p.Current = &current
		p.Total = &total
	}
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Progress{Progress: p},
	}
}

func stageProgressEvent(id StageID, phase progressv1.Phase, message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Progress{Progress: &progressv1.ProgressEvent{
			Message: message,
			Phase:   phase,
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

func logEvent(message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Log{Log: &progressv1.LogEvent{Message: message}},
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
