package server

import deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"

const eventSenderBuffer = 256

type eventSender struct {
	events chan *deploymentsv1.DeployEvent
	done   chan struct{}
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
		if s.err != nil {
			continue
		}
		if err := send(ev); err != nil {
			s.err = err
		}
	}
}

func (s *eventSender) send(ev *deploymentsv1.DeployEvent) {
	s.events <- ev
}

func (s *eventSender) close() error {
	close(s.events)
	<-s.done
	return s.err
}
