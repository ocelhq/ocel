package runs

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	maxReceive          = 100
	receiveWait         = 20 * time.Second
	idleWait            = 2 * time.Second
	firstReceiveBackoff = time.Second
	longestReceiveWait  = 30 * time.Second
	finishingBudget     = 30 * time.Second
)

type heldMessages struct {
	mu      sync.Mutex
	handles map[string]bool
}

func (h *heldMessages) hold(received []Received) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rcv := range received {
		if rcv.Handle != "" {
			h.handles[rcv.Handle] = true
		}
	}
}

func (h *heldMessages) drop(received []Received) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, rcv := range received {
		delete(h.handles, rcv.Handle)
	}
}

func (h *heldMessages) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	handles := make([]string, 0, len(h.handles))
	for handle := range h.handles {
		handles = append(handles, handle)
	}
	return handles
}

func (e *Engine) Serve(ctx context.Context, receiver Receiver, topic, consumer string) {
	attempts, stopAttempts := e.withShutdownGrace(ctx)
	defer stopAttempts()
	held := &heldMessages{handles: map[string]bool{}}
	extending, stopExtending := context.WithCancel(attempts)
	var extender, running sync.WaitGroup
	extender.Go(func() { e.keepHidden(extending, receiver, topic, consumer, held) })
	defer extender.Wait()
	defer stopExtending()
	defer running.Wait()
	consumerSlots := newLocalSlots(e.cfg.InFlight)
	backoff := firstReceiveBackoff
	for ctx.Err() == nil {
		deployed, found := e.consumer(topic, consumer)
		if !found {
			return
		}
		workerSlots := e.workerSlotsOf(deployed.consumer.Worker)
		if workerSlots == nil {
			wait(ctx, idleWait, nil, nil)
			continue
		}
		consumerSlots.setLimit(e.inFlightOf(deployed))
		want := maxReceive
		if deployed.batches() {
			want = 1
		}
		reserved := reserveSlots(want, consumerSlots, workerSlots)
		if reserved.n == 0 {
			wait(ctx, idleWait, reserved.changed[0], reserved.changed[1])
			continue
		}
		received, err := e.gather(ctx, receiver, deployed, reserved.n, held)
		if err != nil {
			reserved.keep(0)
			e.giveBack(attempts, receiver, topic, consumer, received, held)
			if ctx.Err() != nil {
				return
			}
			slog.Warn("receive from a queue", "topic", topic, "consumer", consumer, "error", err)
			wait(ctx, backoff/2+rand.N(backoff/2+1), nil, nil)
			backoff = min(backoff*2, longestReceiveWait)
			continue
		}
		backoff = firstReceiveBackoff
		groups := deliveriesOf(deployed, received)
		reserved.keep(len(groups))
		for _, group := range groups {
			running.Go(func() {
				defer reserved.release(1)
				dispositions := e.Deliver(attempts, topic, consumer, group)
				e.dispose(attempts, receiver, topic, consumer, group, dispositions)
				held.drop(group)
			})
		}
	}
}

func (e *Engine) inFlightOf(deployed deployedConsumer) int {
	if concurrency := deployed.consumer.Concurrency; concurrency > 0 {
		return min(concurrency, e.cfg.InFlight)
	}
	return e.cfg.InFlight
}

func (e *Engine) gather(ctx context.Context, receiver Receiver, deployed deployedConsumer, n int, held *heldMessages) ([]Received, error) {
	req := ReceiveRequest{Topic: deployed.topicName, Consumer: deployed.consumer.Name, Max: n, Wait: receiveWait, Hidden: e.cfg.Lease}
	if !deployed.batches() {
		received, err := receiver.Receive(ctx, req)
		held.hold(received)
		return received, err
	}
	size := deployed.consumer.Batch.Size
	req.Max = size
	gathered, err := receiver.Receive(ctx, req)
	held.hold(gathered)
	if err != nil || len(gathered) == 0 {
		return gathered, err
	}
	deadline := time.Now().Add(deployed.consumer.Batch.Timeout)
	for len(gathered) < size && ctx.Err() == nil {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		req.Max, req.Wait = size-len(gathered), remaining
		more, err := receiver.Receive(ctx, req)
		held.hold(more)
		gathered = append(gathered, more...)
		if err != nil {
			break
		}
	}
	return gathered, nil
}

func deliveriesOf(deployed deployedConsumer, received []Received) [][]Received {
	if len(received) == 0 {
		return nil
	}
	if deployed.batches() {
		return [][]Received{received}
	}
	if !deployed.topic.Ordered {
		deliveries := make([][]Received, 0, len(received))
		for _, rcv := range received {
			deliveries = append(deliveries, []Received{rcv})
		}
		return deliveries
	}
	var order []string
	byGroup := map[string][]Received{}
	for _, rcv := range received {
		rcv.Message.Consumer = deployed.consumer.Name
		group := rcv.Message.group()
		if _, seen := byGroup[group]; !seen {
			order = append(order, group)
		}
		byGroup[group] = append(byGroup[group], rcv)
	}
	deliveries := make([][]Received, 0, len(order))
	for _, group := range order {
		deliveries = append(deliveries, byGroup[group])
	}
	return deliveries
}

func (e *Engine) dispose(ctx context.Context, receiver Receiver, topic, consumer string, received []Received, dispositions []Disposition) {
	finishing, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishingBudget)
	defer cancel()
	var acked []string
	for i, rcv := range received {
		if dispositions[i].Done {
			acked = append(acked, rcv.Handle)
			continue
		}
		if err := receiver.Hide(finishing, topic, consumer, rcv.Handle, dispositions[i].After); err != nil {
			slog.Warn("hide a message until it is redelivered", "topic", topic, "consumer", consumer, "error", err)
		}
	}
	if len(acked) == 0 {
		return
	}
	if err := receiver.Ack(finishing, topic, consumer, acked); err != nil {
		slog.Warn("delete the messages a delivery is finished with", "topic", topic, "consumer", consumer, "error", err)
	}
}

func (e *Engine) giveBack(ctx context.Context, receiver Receiver, topic, consumer string, received []Received, held *heldMessages) {
	if len(received) == 0 {
		return
	}
	dispositions := make([]Disposition, len(received))
	e.dispose(ctx, receiver, topic, consumer, received, dispositions)
	held.drop(received)
}

func (e *Engine) keepHidden(ctx context.Context, receiver Receiver, topic, consumer string, held *heldMessages) {
	ticker := time.NewTicker(e.cfg.Lease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		handles := held.all()
		if len(handles) == 0 {
			continue
		}
		if err := receiver.Extend(ctx, topic, consumer, handles, e.cfg.Lease); err != nil && ctx.Err() == nil {
			slog.Warn("keep a queue's messages hidden while they are worked", "topic", topic, "consumer", consumer, "error", err)
		}
	}
}

func (e *Engine) withShutdownGrace(ctx context.Context) (context.Context, context.CancelFunc) {
	attempts, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopWaiting := context.AfterFunc(ctx, func() {
		grace := time.NewTimer(e.cfg.ShutdownGrace)
		defer grace.Stop()
		select {
		case <-grace.C:
			cancel()
		case <-attempts.Done():
		}
	})
	return attempts, func() {
		stopWaiting()
		cancel()
	}
}

func wait(ctx context.Context, interval time.Duration, changes ...<-chan struct{}) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	var first, second <-chan struct{}
	if len(changes) > 0 {
		first = changes[0]
	}
	if len(changes) > 1 {
		second = changes[1]
	}
	select {
	case <-ctx.Done():
	case <-first:
	case <-second:
	case <-timer.C:
	}
}
