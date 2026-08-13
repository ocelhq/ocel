package server

import (
	"context"
	"sync"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

const eventSenderBuffer = 256

type eventSender struct {
	events chan *deploymentsv1.DeployEvent
	done   chan struct{}
	ctx    context.Context

	mu     sync.RWMutex
	closed bool
	err    error
}

func newEventSender(ctx context.Context, send func(*deploymentsv1.DeployEvent) error) *eventSender {
	s := &eventSender{
		events: make(chan *deploymentsv1.DeployEvent, eventSenderBuffer),
		done:   make(chan struct{}),
		ctx:    ctx,
	}
	go s.drain(send)
	return s
}

func (s *eventSender) drain(send func(*deploymentsv1.DeployEvent) error) {
	defer close(s.done)
	for ev := range s.events {
		if err := send(ev); err != nil && s.err == nil {
			s.err = err
		}
	}
}

func (s *eventSender) send(ev *deploymentsv1.DeployEvent) {
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

func (s *eventSender) close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	close(s.events)
	<-s.done
	return s.err
}

func newDeployReporter(sender *eventSender, stages deployStages) (deploy.Progress, func(deploy.StageID) func(string), func(string)) {
	byPhase := map[deploymentsv1.Phase]deploy.StageID{
		deploymentsv1.Phase_PHASE_UNSPECIFIED:  stages.preparing.ID,
		deploymentsv1.Phase_PHASE_UPLOADING:    stages.uploading.ID,
		deploymentsv1.Phase_PHASE_PROVISIONING: stages.provisioning.ID,
		deploymentsv1.Phase_PHASE_FINALIZING:   stages.finalizing.ID,
	}
	progress := func(phase deploymentsv1.Phase, m string, current, total uint32) {
		var id []byte
		if stageID, ok := byPhase[phase]; ok {
			id = stageID[:]
		}
		sender.send(phaseProgressEvent(id, phase, m, current, total))
	}
	stageReport := func(id deploy.StageID) func(string) {
		return func(m string) { sender.send(stageProgressEvent(id, deploymentsv1.Phase_PHASE_PROVISIONING, m)) }
	}
	logf := func(m string) { sender.send(logEvent(m)) }
	return progress, stageReport, logf
}

func newTeardownReporter(sender *eventSender) (func(deploy.StageID) func(string), func(string)) {
	stageReport := func(id deploy.StageID) func(string) {
		return func(m string) { sender.send(stageProgressEvent(id, deploymentsv1.Phase_PHASE_DELETING, m)) }
	}
	logf := func(m string) { sender.send(logEvent(m)) }
	return stageReport, logf
}
