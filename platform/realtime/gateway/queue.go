package gateway

import "sync"

type queue struct {
	budgetBytes int

	mu          sync.Mutex
	frames      [][]byte
	queuedBytes int
	isOverflow  bool
	ready       chan struct{}
	overflowed  chan struct{}
}

func newQueue(budgetBytes int) *queue {
	return &queue{budgetBytes: budgetBytes, ready: make(chan struct{}, 1), overflowed: make(chan struct{})}
}

func (q *queue) Offer(frame []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.isOverflow {
		return
	}
	if len(q.frames) > 0 && q.queuedBytes+len(frame) > q.budgetBytes {
		q.isOverflow = true
		q.frames, q.queuedBytes = nil, 0
		close(q.overflowed)
		return
	}
	q.frames = append(q.frames, frame)
	q.queuedBytes += len(frame)
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *queue) Take() [][]byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	frames := q.frames
	q.frames, q.queuedBytes = nil, 0
	return frames
}

func (q *queue) Ready() <-chan struct{} { return q.ready }

func (q *queue) Overflowed() <-chan struct{} { return q.overflowed }
