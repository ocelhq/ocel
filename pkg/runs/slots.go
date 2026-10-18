package runs

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	slotWait    = time.Second
	maxSlotHold = 5 * time.Minute
)

type SlotSet struct {
	Name  string
	Limit int
}

type Slot struct {
	Set    SlotSet
	Index  string
	Holder string
}

type Slots interface {
	Take(ctx context.Context, set SlotSet, holder string, until time.Time) (Slot, bool, error)
	Renew(ctx context.Context, slot Slot, until time.Time) error
	Release(ctx context.Context, slot Slot) error
}

func slotHold(receives int) time.Duration {
	hold := slotWait + time.Duration(rand.Int64N(int64(slotWait)))
	return min(max(hold, time.Duration(receives)*slotWait), maxSlotHold)
}

func (e *Engine) slotSets(deployed deployedConsumer) []SlotSet {
	var sets []SlotSet
	if limit := deployed.consumer.Concurrency; limit > 0 {
		sets = append(sets, SlotSet{Name: "consumer#" + deployed.topicName + "#" + deployed.consumer.Name, Limit: limit})
	}
	if limit := e.worker(deployed.consumer.Worker).Concurrency; limit > 0 {
		sets = append(sets, SlotSet{Name: "worker#" + deployed.consumer.Worker, Limit: limit})
	}
	return sets
}

func (e *Engine) takeSlots(ctx context.Context, deployed deployedConsumer, holder string) ([]Slot, bool, error) {
	if e.cfg.Slots == nil {
		return nil, true, nil
	}
	until := time.Now().Add(e.cfg.Lease)
	var taken []Slot
	for _, set := range e.slotSets(deployed) {
		slot, ok, err := e.cfg.Slots.Take(ctx, set, holder, until)
		if err != nil || !ok {
			e.releaseSlots(ctx, taken)
			return nil, false, err
		}
		taken = append(taken, slot)
	}
	return taken, true, nil
}

func (e *Engine) renewSlots(ctx context.Context, taken []Slot) {
	until := time.Now().Add(e.cfg.Lease)
	for _, slot := range taken {
		if err := e.cfg.Slots.Renew(ctx, slot, until); err != nil && ctx.Err() == nil {
			slog.Warn("renew the lease of a slot being worked", "set", slot.Set.Name, "error", err)
		}
	}
}

func (e *Engine) releaseSlots(ctx context.Context, taken []Slot) {
	for _, slot := range taken {
		if err := e.cfg.Slots.Release(context.WithoutCancel(ctx), slot); err != nil {
			slog.Warn("release a slot", "set", slot.Set.Name, "error", err)
		}
	}
}

type localSlots struct {
	mu      sync.Mutex
	limit   int
	used    int
	changed chan struct{}
}

func newLocalSlots(limit int) *localSlots {
	return &localSlots{limit: limit, changed: make(chan struct{})}
}

func (s *localSlots) setLimit(limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limit != limit {
		s.limit = limit
		s.broadcast()
	}
}

func (s *localSlots) changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func (s *localSlots) reserve(want int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	granted := want
	if s.limit > 0 {
		granted = min(want, max(s.limit-s.used, 0))
	}
	s.used += granted
	return granted
}

func (s *localSlots) unreserve(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used -= n
}

func (s *localSlots) release(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.used -= n
	s.broadcast()
}

func (s *localSlots) broadcast() {
	close(s.changed)
	s.changed = make(chan struct{})
}

type reservation struct {
	consumer, worker *localSlots
	n                int
	changed          [2]<-chan struct{}
}

func reserveSlots(want int, consumer, worker *localSlots) *reservation {
	held := &reservation{consumer: consumer, worker: worker, changed: [2]<-chan struct{}{consumer.changes(), worker.changes()}}
	granted := consumer.reserve(want)
	if granted == 0 {
		return held
	}
	held.n = worker.reserve(granted)
	consumer.unreserve(granted - held.n)
	return held
}

func (r *reservation) keep(n int) {
	r.consumer.unreserve(r.n - n)
	r.worker.unreserve(r.n - n)
	r.n = n
}

func (r *reservation) release(n int) {
	r.consumer.release(n)
	r.worker.release(n)
}
