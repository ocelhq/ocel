package server

import (
	"context"
	"sync"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
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

func newDeployReporter(sender *eventSender, stages deployStages) (deploy.Progress, func(deploy.StageID) func(string), func(string), func(edge.Need, string)) {
	byPhase := map[progressv1.Phase]deploy.StageID{
		progressv1.Phase_PHASE_UNSPECIFIED:  stages.preparing.ID,
		progressv1.Phase_PHASE_UPLOADING:    stages.uploading.ID,
		progressv1.Phase_PHASE_PROVISIONING: stages.provisioning.ID,
		progressv1.Phase_PHASE_FINALIZING:   stages.finalizing.ID,
	}
	progress := func(phase progressv1.Phase, m string, current, total uint32) {
		var id []byte
		if stageID, ok := byPhase[phase]; ok {
			id = stageID[:]
		}
		sender.send(phaseProgressEvent(id, phase, m, current, total))
	}
	stageReport := func(id deploy.StageID) func(string) {
		return func(m string) { sender.send(stageProgressEvent(id, progressv1.Phase_PHASE_PROVISIONING, m)) }
	}
	logf := func(m string) { sender.send(logEvent(m)) }
	degraded := func(need edge.Need, detail string) { sender.send(degradedEvent(need, detail)) }
	return progress, stageReport, logf, degraded
}

func newTeardownReporter(sender *eventSender) (func(deploy.StageID) func(string), func(string)) {
	stageReport := func(id deploy.StageID) func(string) {
		return func(m string) { sender.send(stageProgressEvent(id, progressv1.Phase_PHASE_DELETING, m)) }
	}
	logf := func(m string) { sender.send(logEvent(m)) }
	return stageReport, logf
}
