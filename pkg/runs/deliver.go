package runs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

var errRunGone = errors.New("the run this message names no longer exists")

type claim struct {
	run Run
	rcv Received
}

func (e *Engine) Deliver(ctx context.Context, topic, consumer string, received []Received) []Disposition {
	dispositions := make([]Disposition, len(received))
	deployed, found := e.consumer(topic, consumer)
	if !found || !e.serves(deployed.consumer.Worker) {
		slog.Warn("a message arrived for a consumer this deployment does not run", "topic", topic, "consumer", consumer)
		for i := range dispositions {
			dispositions[i] = redeliverAfter(retainedFor)
		}
		return dispositions
	}
	for i := range received {
		received[i].Message.Topic, received[i].Message.Consumer = topic, consumer
	}
	switch {
	case deployed.batches():
		e.deliverBatch(ctx, deployed, received, dispositions)
	case deployed.topic.Ordered:
		blocked := map[string]bool{}
		for i, rcv := range received {
			group := rcv.Message.group()
			if blocked[group] {
				dispositions[i] = redeliverAfter(0)
				continue
			}
			dispositions[i] = e.deliverOne(ctx, deployed, rcv)
			blocked[group] = !dispositions[i].Done
		}
	default:
		for i, rcv := range received {
			dispositions[i] = e.deliverOne(ctx, deployed, rcv)
		}
	}
	return dispositions
}

func (e *Engine) deliverOne(ctx context.Context, deployed deployedConsumer, rcv Received) Disposition {
	run, err := e.arrive(ctx, deployed, rcv.Message)
	if errors.Is(err, errRunGone) {
		return done
	}
	if err != nil {
		slog.Warn("read the run a message names", "topic", deployed.topicName, "consumer", deployed.consumer.Name, "error", err)
		return redeliverAfter(retainedFor)
	}
	run, disposition, admitted := e.admit(ctx, deployed, rcv, run)
	if !admitted {
		return disposition
	}
	slots, taken, err := e.takeSlots(ctx, deployed, run.Execution)
	if err != nil {
		slog.Warn("take a concurrency slot", "execution", run.Execution, "error", err)
		return redeliverAfter(retainedFor)
	}
	if !taken {
		return e.redeliverUnlessUndeliverable(ctx, run, rcv, slotHold(rcv.Receives))
	}
	defer e.releaseSlots(ctx, slots)
	claimed, disposition, ok := e.claim(ctx, deployed, rcv, run)
	if !ok {
		return disposition
	}
	return e.attemptAndFinish(ctx, deployed, []claim{{run: claimed, rcv: rcv}}, slots)[0]
}

func (e *Engine) deliverBatch(ctx context.Context, deployed deployedConsumer, received []Received, dispositions []Disposition) {
	type candidate struct {
		index int
		run   Run
	}
	var admitted []candidate
	for i, rcv := range received {
		run, err := e.arrive(ctx, deployed, rcv.Message)
		switch {
		case errors.Is(err, errRunGone):
			dispositions[i] = done
		case err != nil:
			slog.Warn("read the run a message names", "topic", deployed.topicName, "consumer", deployed.consumer.Name, "error", err)
			dispositions[i] = redeliverAfter(retainedFor)
		default:
			var ok bool
			if run, dispositions[i], ok = e.admit(ctx, deployed, rcv, run); ok {
				admitted = append(admitted, candidate{index: i, run: run})
			}
		}
	}
	if len(admitted) == 0 {
		return
	}
	slots, taken, err := e.takeSlots(ctx, deployed, admitted[0].run.Execution)
	if err != nil || !taken {
		if err != nil {
			slog.Warn("take a concurrency slot", "execution", admitted[0].run.Execution, "error", err)
		}
		for _, c := range admitted {
			dispositions[c.index] = e.redeliverUnlessUndeliverable(ctx, c.run, received[c.index], slotHold(received[c.index].Receives))
		}
		return
	}
	defer e.releaseSlots(ctx, slots)
	var claims []claim
	var indexes []int
	for _, c := range admitted {
		claimed, disposition, ok := e.claim(ctx, deployed, received[c.index], c.run)
		if !ok {
			dispositions[c.index] = disposition
			continue
		}
		claims = append(claims, claim{run: claimed, rcv: received[c.index]})
		indexes = append(indexes, c.index)
	}
	if len(claims) == 0 {
		return
	}
	for i, disposition := range e.attemptAndFinish(ctx, deployed, claims, slots) {
		dispositions[indexes[i]] = disposition
	}
}

func (e *Engine) arrive(ctx context.Context, deployed deployedConsumer, msg Message) (Run, error) {
	execution := msg.executionFor(deployed.consumer.Name)
	run, err := e.cfg.Store.ReadRun(ctx, deployed.topicName, execution)
	if err == nil || !errors.Is(err, keyvalue.ErrNotFound) {
		return run, err
	}
	if msg.Execution != "" {
		return Run{}, errRunGone
	}
	created := runOfMessage(deployed, msg, time.Now()).truncated()
	revision, err := e.cfg.Store.CreateRun(ctx, created)
	if errors.Is(err, keyvalue.ErrStale) {
		return e.cfg.Store.ReadRun(ctx, deployed.topicName, execution)
	}
	if err != nil {
		return Run{}, err
	}
	created.Revision = revision
	return created, nil
}

func runOfMessage(deployed deployedConsumer, msg Message, now time.Time) Run {
	run := Run{
		Execution:   msg.executionFor(deployed.consumer.Name),
		Topic:       deployed.topicName,
		Consumer:    deployed.consumer.Name,
		Status:      provider.RunQueued,
		MaxAttempts: AttemptsFor(deployed.consumer.Retry, int32(msg.MaxAttempts)),
		Message:     msg.ID,
		PublishedAt: msg.PublishedAt,
		Key:         msg.Key,
		Lane:        LaneFor(deployed.consumer.Lanes, msg.Lane),
		Delivery:    msg.Delivery,
		CreatedAt:   msg.PublishedAt,
		DueAt:       msg.DueAt,
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.DueAt.IsZero() {
		run.DueAt = run.CreatedAt
	}
	if run.DueAt.After(now) {
		run.Status = provider.RunDelayed
	}
	if ttl := deployed.topic.TTL; ttl > 0 {
		run.ExpiresAt = run.DueAt.Add(ttl)
	}
	if deployed.isTask() {
		run.Payload = msg.Payload
	}
	return run
}

func (e *Engine) admit(ctx context.Context, deployed deployedConsumer, rcv Received, run Run) (Run, Disposition, bool) {
	for range maxRaces {
		now := time.Now()
		ready, wait := readinessOf(run, rcv.Message, now)
		changed := run
		switch ready {
		case stale:
			return run, done, false
		case runnable:
			return run, Disposition{}, true
		case held:
			return run, e.redeliverUnlessUndeliverable(ctx, run, rcv, wait), false
		case early:
			if !deployed.topic.Ordered || rcv.Handle == "" || run.Held == rcv.Handle {
				return run, e.redeliverUnlessUndeliverable(ctx, run, rcv, wait), false
			}
			changed.Held = rcv.Handle
		case expired:
			changed.settle(provider.RunExpired, nil, "", nil, now)
		case exhausted:
			changed.settle(provider.RunFailed, nil, exhaustedReason(run), rcv.Message.Payload, now)
		}
		written, err := e.write(ctx, changed)
		if errors.Is(err, keyvalue.ErrStale) {
			if run, err = e.reread(ctx, run); err != nil {
				return run, e.retainOn(err), false
			}
			continue
		}
		if err != nil {
			return run, e.retainOn(err), false
		}
		if ready == early {
			return written, e.redeliverUnlessUndeliverable(ctx, written, rcv, wait), false
		}
		return written, done, false
	}
	return run, redeliverAfter(retainedFor), false
}

func (e *Engine) claim(ctx context.Context, deployed deployedConsumer, rcv Received, run Run) (Run, Disposition, bool) {
	for range maxRaces {
		claimed := run
		claimed.claim(time.Now(), e.cfg.Lease)
		written, err := e.write(ctx, claimed)
		if err == nil {
			return written, Disposition{}, true
		}
		if !errors.Is(err, keyvalue.ErrStale) {
			return run, e.retainOn(err), false
		}
		if run, err = e.reread(ctx, run); err != nil {
			return run, e.retainOn(err), false
		}
		var disposition Disposition
		var ok bool
		if run, disposition, ok = e.admit(ctx, deployed, rcv, run); !ok {
			return run, disposition, false
		}
	}
	return run, redeliverAfter(retainedFor), false
}

func (e *Engine) redeliverUnlessUndeliverable(ctx context.Context, run Run, rcv Received, wait time.Duration) Disposition {
	if e.cfg.MaxReceives <= 0 || rcv.Receives < e.cfg.MaxReceives || !IsWaiting(run.Status) {
		return redeliverAfter(wait)
	}
	failed := run
	failed.settle(provider.RunFailed, nil, fmt.Sprintf("its message was delivered %d times without the run starting, the most its queue allows before dead-lettering it", rcv.Receives), rcv.Message.Payload, time.Now())
	if _, err := e.write(context.WithoutCancel(ctx), failed); err != nil {
		if !errors.Is(err, keyvalue.ErrStale) {
			slog.Warn("fail a run its queue is about to dead-letter", "execution", run.Execution, "error", err)
		}
		return redeliverAfter(wait)
	}
	return done
}

func (e *Engine) retainOn(err error) Disposition {
	slog.Warn("record a run's state", "error", err)
	return redeliverAfter(retainedFor)
}

func (e *Engine) write(ctx context.Context, run Run) (Run, error) {
	run = run.truncated()
	revision, err := e.cfg.Store.WriteRun(ctx, run)
	if err != nil {
		return run, err
	}
	run.Revision = revision
	return run, nil
}

func (e *Engine) reread(ctx context.Context, run Run) (Run, error) {
	fresh, err := e.cfg.Store.ReadRun(ctx, run.Topic, run.Execution)
	if errors.Is(err, keyvalue.ErrNotFound) {
		return run, errRunGone
	}
	return fresh, err
}

func (e *Engine) attemptAndFinish(ctx context.Context, deployed deployedConsumer, claims []claim, slots []Slot) []Disposition {
	started := time.Now()
	answer := e.attempt(ctx, deployed, claims, slots)
	took := time.Since(started)
	dispositions := make([]Disposition, len(claims))
	for i, c := range claims {
		dispositions[i] = e.finish(ctx, deployed, c, answer, took)
	}
	return dispositions
}

func (e *Engine) attempt(ctx context.Context, deployed deployedConsumer, claims []claim, slots []Slot) Answer {
	attemptCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	if maxDuration := deployed.consumer.MaxDuration; maxDuration > 0 {
		var stop context.CancelFunc
		attemptCtx, stop = context.WithTimeoutCause(attemptCtx, maxDuration, ErrTimedOut)
		defer stop()
	}
	single := len(claims) == 1
	if single {
		e.track(claims[0].run.Execution, cancel)
		defer e.untrack(claims[0].run.Execution)
	}
	posted := envelopeOf(deployed, claims)
	watching, stopWatching := context.WithCancel(context.WithoutCancel(ctx))
	var watchers sync.WaitGroup
	watchers.Go(func() { e.keepLeases(watching, claims, slots, single, cancel) })
	if single && e.cfg.CancelPoll > 0 {
		watchers.Go(func() { e.watchForCancel(watching, claims[0].run, cancel) })
	}
	worker := e.worker(deployed.consumer.Worker)
	client := worker.Client
	if client == nil {
		client = http.DefaultClient
	}
	answer := PostEnvelope(ctx, attemptCtx, client, worker.URL, posted)
	stopWatching()
	watchers.Wait()
	return answer
}

func (e *Engine) keepLeases(ctx context.Context, claims []claim, slots []Slot, single bool, cancel context.CancelCauseFunc) {
	ticker := time.NewTicker(e.cfg.Lease / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for i := range claims {
			if !e.renewLease(ctx, &claims[i]) && single {
				cancel(ErrCanceled)
			}
		}
		if e.cfg.Slots != nil {
			e.renewSlots(ctx, slots)
		}
	}
}

func (e *Engine) renewLease(ctx context.Context, c *claim) bool {
	for range maxRaces {
		renewed := c.run
		renewed.LeaseUntil = time.Now().Add(e.cfg.Lease)
		written, err := e.write(ctx, renewed)
		if err == nil {
			c.run = written
			return true
		}
		if !errors.Is(err, keyvalue.ErrStale) {
			if ctx.Err() == nil {
				slog.Warn("renew the lease of a run being worked", "execution", c.run.Execution, "error", err)
			}
			return true
		}
		fresh, err := e.reread(ctx, c.run)
		if err != nil {
			return !errors.Is(err, errRunGone)
		}
		if !fresh.owns(c.run) {
			return false
		}
		c.run = fresh
	}
	return true
}

func (e *Engine) watchForCancel(ctx context.Context, claimed Run, cancel context.CancelCauseFunc) {
	ticker := time.NewTicker(e.cfg.CancelPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		fresh, err := e.reread(ctx, claimed)
		if errors.Is(err, errRunGone) || (err == nil && fresh.Status == provider.RunCanceled) {
			cancel(ErrCanceled)
			return
		}
	}
}

func (e *Engine) finish(ctx context.Context, deployed deployedConsumer, c claim, answer Answer, took time.Duration) Disposition {
	ctx = context.WithoutCancel(ctx)
	v, acts := verdictOn(c.run, answer, deployed.isTask(), deployed.consumer.Retry)
	if !acts {
		return done
	}
	current := c.run
	for range maxRaces {
		changed := current
		changed.apply(v, c.rcv.Message.Payload, time.Now())
		_, err := e.write(ctx, changed)
		if errors.Is(err, keyvalue.ErrStale) {
			fresh, err := e.reread(ctx, current)
			if err != nil || !fresh.owns(c.run) {
				return done
			}
			current = fresh
			continue
		}
		if err != nil {
			slog.Warn("record an attempt's outcome", "execution", c.run.Execution, "error", err)
			return redeliverAfter(retainedFor)
		}
		if answer.Outcome != Interrupted || !v.retries() {
			e.cfg.ReportAttempt(Attempt{
				Topic:       deployed.topicName,
				Consumer:    deployed.consumer.Name,
				IsTask:      deployed.isTask(),
				Execution:   c.run.Execution,
				Number:      c.run.Attempts,
				MaxAttempts: c.run.MaxAttempts,
				Status:      v.status,
				Took:        took,
				Reason:      v.reason,
			})
		}
		if v.retries() {
			return redeliverAfter(v.retryAfter)
		}
		return done
	}
	return redeliverAfter(retainedFor)
}

func envelopeOf(deployed deployedConsumer, claims []claim) *topicv1.Envelope {
	posted := &topicv1.Envelope{
		V:        EnvelopeVersion,
		Topic:    deployed.topicName,
		Consumer: deployed.consumer.Name,
		Schema:   SchemaOf(deployed.topic.Schema),
	}
	deliveries := make([]*topicv1.Delivery, 0, len(claims))
	for _, c := range claims {
		payload := c.run.Payload
		if len(payload) == 0 {
			payload = c.rcv.Message.Payload
		}
		deliveries = append(deliveries, &topicv1.Delivery{
			Execution: c.run.Execution,
			Message:   &topicv1.Message{Id: c.run.Message, PublishedAt: TimestampOf(c.run.PublishedAt)},
			Attempt:   &topicv1.Attempt{Number: int32(c.run.Attempts), Of: int32(c.run.MaxAttempts), FirstAttemptedAt: TimestampOf(c.run.StartedAt)},
			Payload:   payload,
		})
	}
	if deployed.batches() {
		posted.Messages = deliveries
		return posted
	}
	only := deliveries[0]
	posted.Execution, posted.Message, posted.Attempt, posted.Payload = only.GetExecution(), only.GetMessage(), only.GetAttempt(), only.GetPayload()
	return posted
}
