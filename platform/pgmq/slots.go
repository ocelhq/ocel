package pgmq

import "sync"

type slots struct {
	mu      sync.Mutex
	limit   int
	used    int
	changed chan struct{}
}

func newSlots(limit int) *slots {
	return &slots{limit: limit, changed: make(chan struct{})}
}

func (s *slots) setLimit(limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limit != limit {
		s.limit = limit
		s.broadcast()
	}
}

func (s *slots) changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func (s *slots) reserve(want int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	granted := want
	if s.limit > 0 {
		granted = min(want, max(s.limit-s.used, 0))
	}
	s.used += granted
	return granted
}

func (s *slots) unreserve(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used -= n
}

func (s *slots) release(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used -= n
	s.broadcast()
}

func (s *slots) broadcast() {
	close(s.changed)
	s.changed = make(chan struct{})
}
