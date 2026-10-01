package gateway

import "sync"

type Queue struct {
	budget int

	mu         sync.Mutex
	frames     [][]byte
	queued     int
	isOverflow bool
	ready      chan struct{}
	overflowed chan struct{}
}

func NewQueue(budget int) *Queue {
	return &Queue{budget: budget, ready: make(chan struct{}, 1), overflowed: make(chan struct{})}
}

func (q *Queue) Offer(frame []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.isOverflow {
		return
	}
	if len(q.frames) > 0 && q.queued+len(frame) > q.budget {
		q.isOverflow = true
		q.frames, q.queued = nil, 0
		close(q.overflowed)
		return
	}
	q.frames = append(q.frames, frame)
	q.queued += len(frame)
	select {
	case q.ready <- struct{}{}:
	default:
	}
}

func (q *Queue) Take() [][]byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	frames := q.frames
	q.frames, q.queued = nil, 0
	return frames
}

func (q *Queue) Ready() <-chan struct{} { return q.ready }

func (q *Queue) Overflowed() <-chan struct{} { return q.overflowed }
