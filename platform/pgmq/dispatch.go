package pgmq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	lease          = 60 * time.Second
	leaseRenewal   = 20 * time.Second
	pollInterval   = 200 * time.Millisecond
	maxReadPerPoll = 100
	maxInFlight    = 1000
)

type message struct {
	id   int64
	body queueMessage
}

type queueLoop struct {
	name      string
	consumers *slots
	lanes     laneTurns

	mu   sync.Mutex
	held map[int64]bool
}

func (loop *queueLoop) hold(messages []message) {
	loop.mu.Lock()
	defer loop.mu.Unlock()
	for _, msg := range messages {
		loop.held[msg.id] = true
	}
}

func (loop *queueLoop) drop(id int64) {
	loop.mu.Lock()
	defer loop.mu.Unlock()
	delete(loop.held, id)
}

func (loop *queueLoop) heldIDs() []int64 {
	loop.mu.Lock()
	defer loop.mu.Unlock()
	ids := make([]int64, 0, len(loop.held))
	for id := range loop.held {
		ids = append(ids, id)
	}
	return ids
}

func (e *Engine) Dispatch(ctx context.Context) error {
	var wg sync.WaitGroup
	loops := map[string]context.CancelFunc{}
	workers := map[string]*slots{}
	background := func(fn func(ctx context.Context)) {
		wg.Go(func() { fn(ctx) })
	}
	background(e.sweep)
	background(e.fireSchedules)
	for {
		deployment := e.current()
		deployedConsumers := deployment.consumers()
		for queue, stop := range loops {
			if _, still := deployedConsumers[queue]; !still {
				stop()
				delete(loops, queue)
			}
		}
		for name, worker := range deployment.Workers {
			if existing, found := workers[name]; found {
				existing.setLimit(worker.Concurrency)
			} else {
				workers[name] = newSlots(worker.Concurrency)
			}
		}
		for queue := range deployedConsumers {
			if _, running := loops[queue]; running {
				continue
			}
			queueCtx, stop := context.WithCancel(ctx)
			loops[queue] = stop
			wg.Go(func() { e.serveQueue(queueCtx, queue, workers) })
		}
		select {
		case <-ctx.Done():
			for _, stop := range loops {
				stop()
			}
			wg.Wait()
			return ctx.Err()
		case <-e.applied:
		}
	}
}

func (e *Engine) serveQueue(ctx context.Context, queue string, workers map[string]*slots) {
	loop := &queueLoop{name: queue, consumers: newSlots(maxInFlight), held: map[int64]bool{}}
	var running sync.WaitGroup
	defer running.Wait()
	running.Go(func() { e.renewLeases(ctx, loop) })
	wake := e.wake(queue)
	for ctx.Err() == nil {
		deployment := e.current()
		deployed, found := deployment.consumers()[queue]
		if !found {
			return
		}
		worker, served := deployment.Workers[deployed.consumer.GetWorker()]
		workerSlots := workers[deployed.consumer.GetWorker()]
		if !served || workerSlots == nil {
			wait(ctx, wake, nil, nil)
			continue
		}
		loop.consumers.setLimit(concurrencyOf(deployed))
		want := maxReadPerPoll
		if deployed.consumer.GetBatch().GetSize() > 0 {
			want = 1
		}
		n, changed := reserveBoth(loop.consumers, workerSlots, want)
		if n == 0 {
			wait(ctx, wake, changed[0], changed[1])
			continue
		}
		messages, err := e.read(ctx, loop, deployed, n)
		if err != nil && ctx.Err() == nil {
			slog.Warn("read a queue", "queue", queue, "error", err)
		}
		if len(messages) == 0 {
			loop.consumers.unreserve(n)
			workerSlots.unreserve(n)
			wait(ctx, wake, nil, nil)
			continue
		}
		loop.hold(messages)
		deliveries := [][]message{messages}
		if deployed.consumer.GetBatch().GetSize() == 0 {
			deliveries = deliveries[:0]
			for _, msg := range messages {
				deliveries = append(deliveries, []message{msg})
			}
		}
		if unused := n - len(deliveries); unused > 0 {
			loop.consumers.unreserve(unused)
			workerSlots.unreserve(unused)
		}
		for _, delivery := range deliveries {
			running.Go(func() {
				defer loop.consumers.release(1)
				defer workerSlots.release(1)
				e.deliver(ctx, loop, deployed, worker, delivery)
			})
		}
	}
}

func reserveBoth(consumer, worker *slots, want int) (int, [2]<-chan struct{}) {
	consumerChanged, workerChanged := consumer.changes(), worker.changes()
	granted := consumer.reserve(want)
	if granted == 0 {
		return 0, [2]<-chan struct{}{consumerChanged, workerChanged}
	}
	both := worker.reserve(granted)
	if both < granted {
		consumer.unreserve(granted - both)
	}
	return both, [2]<-chan struct{}{consumerChanged, workerChanged}
}

func concurrencyOf(deployed deployedConsumer) int {
	if concurrency := int(deployed.consumer.GetConcurrency()); concurrency > 0 {
		return min(concurrency, maxInFlight)
	}
	return maxInFlight
}

func wait(ctx context.Context, wake <-chan struct{}, a, b <-chan struct{}) {
	timer := time.NewTimer(pollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-wake:
	case <-a:
	case <-b:
	case <-timer.C:
	}
}

func (e *Engine) renewLeases(ctx context.Context, loop *queueLoop) {
	ticker := time.NewTicker(leaseRenewal)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ids := loop.heldIDs(); len(ids) > 0 {
			if _, err := e.pool.Exec(ctx, "SELECT pgmq.set_vt($1, $2::bigint[], clock_timestamp() + make_interval(secs => $3))", loop.name, ids, lease.Seconds()); err != nil && ctx.Err() == nil {
				slog.Warn("renew the leases of a queue's messages", "queue", loop.name, "error", err)
			}
		}
	}
}

func (e *Engine) read(ctx context.Context, loop *queueLoop, deployed deployedConsumer, n int) ([]message, error) {
	vt := int(lease.Seconds())
	size := int(deployed.consumer.GetBatch().GetSize())
	switch {
	case deployed.topic.GetOrdered() && size > 0:
		return e.collectBatch(ctx, loop, deployed, size, func(want int) ([]message, error) {
			return e.readWith(ctx, "SELECT msg_id, message FROM pgmq.read_grouped($1, $2, $3)", loop.name, vt, want)
		})
	case deployed.topic.GetOrdered():
		return e.readWith(ctx, "SELECT msg_id, message FROM pgmq.read_grouped_head($1, $2, $3)", loop.name, vt, n)
	case size > 0:
		return e.collectBatch(ctx, loop, deployed, size, func(want int) ([]message, error) { return e.readLanes(ctx, loop, deployed, want) })
	default:
		return e.readLanes(ctx, loop, deployed, n)
	}
}

func (e *Engine) collectBatch(ctx context.Context, loop *queueLoop, deployed deployedConsumer, size int, read func(want int) ([]message, error)) ([]message, error) {
	collected, err := read(size)
	if err != nil || len(collected) == 0 || len(collected) >= size {
		return collected, err
	}
	loop.hold(collected)
	timeout := deployed.consumer.GetBatch().GetTimeout().AsDuration()
	deadline := time.Now().Add(timeout)
	for len(collected) < size && time.Now().Before(deadline) {
		timer := time.NewTimer(min(pollInterval, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return collected, nil
		case <-timer.C:
		}
		more, err := read(size - len(collected))
		if err != nil {
			return collected, nil
		}
		loop.hold(more)
		collected = append(collected, more...)
	}
	return collected, nil
}

func (e *Engine) readLanes(ctx context.Context, loop *queueLoop, deployed deployedConsumer, n int) ([]message, error) {
	vt := int(lease.Seconds())
	var read []message
	for lane, share := range loop.lanes.share(lanesOf(deployed.consumer), n) {
		condition, err := json.Marshal(map[string]string{"lane": laneName(lane)})
		if err != nil {
			return nil, err
		}
		got, err := e.readWith(ctx, "SELECT msg_id, message FROM pgmq.read($1, $2, $3, $4::jsonb)", loop.name, vt, share, string(condition))
		if err != nil {
			return read, err
		}
		read = append(read, got...)
	}
	if rest := n - len(read); rest > 0 {
		got, err := e.readWith(ctx, "SELECT msg_id, message FROM pgmq.read($1, $2, $3)", loop.name, vt, rest)
		if err != nil {
			return read, err
		}
		read = append(read, got...)
	}
	return read, nil
}

func (e *Engine) readWith(ctx context.Context, query string, args ...any) ([]message, error) {
	rows, err := e.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (message, error) {
		var msg message
		var body []byte
		if err := row.Scan(&msg.id, &body); err != nil {
			return msg, err
		}
		if err := json.Unmarshal(body, &msg.body); err != nil {
			return msg, fmt.Errorf("message %d is not one the engine sent: %w", msg.id, err)
		}
		return msg, nil
	})
}
