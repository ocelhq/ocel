package server

import (
	"sync"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

const eventSenderBuffer = 256

type eventSender struct {
	events chan *deploymentsv1.DeployEvent
	done   chan struct{}

	mu     sync.RWMutex
	closed bool
	err    error
}

func newEventSender(send func(*deploymentsv1.DeployEvent) error) *eventSender {
	s := &eventSender{
		events: make(chan *deploymentsv1.DeployEvent, eventSenderBuffer),
		done:   make(chan struct{}),
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
	s.events <- ev
}

func (s *eventSender) close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	close(s.events)
	<-s.done
	return s.err
}
