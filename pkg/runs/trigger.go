package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type runRequest struct {
	payload     json.RawMessage
	dueAt       time.Time
	key         string
	lane        provider.Lane
	maxAttempts int32
	ttl         time.Duration
	tags        []string
	metadata    json.RawMessage
}

func refuseInvalidTrigger(payload []byte, options *taskv1.TriggerOptions) error {
	if err := RefuseNonJSON(payload); err != nil {
		return err
	}
	if metadata := options.GetMetadata(); len(metadata) > 0 {
		return RefuseNonObject(metadata)
	}
	return nil
}

func (e *Engine) trigger(ctx context.Context, deployed deployedConsumer, payload []byte, options *taskv1.TriggerOptions) (string, error) {
	if err := refuseInvalidTrigger(payload, options); err != nil {
		return "", err
	}
	now := time.Now()
	req := runRequest{
		payload:     payload,
		dueAt:       now,
		key:         options.GetKey(),
		lane:        LaneOf(options.GetLane()),
		maxAttempts: options.GetMaxAttempts(),
		ttl:         deployed.topic.TTL,
		tags:        options.GetTags(),
		metadata:    options.GetMetadata(),
	}
	if options.GetDueAt() != nil {
		req.dueAt = options.GetDueAt().AsTime()
	}
	if options.GetTtl() != nil {
		req.ttl = options.GetTtl().AsDuration()
	}
	debounce := options.GetDebounce()
	if debounce != nil {
		req.dueAt = now.Add(debounce.GetDelay().AsDuration())
	}
	if err := e.refuseDelay(deployed.topicName, deployed.topic, req.dueAt, now); err != nil {
		return "", err
	}
	messageID := NewMessageID(now)
	execution := ExecutionOf(messageID, deployed.consumer.Name)
	var idempotency provider.ExpiringRecord
	if key := options.GetIdempotencyKey(); key != "" {
		life := provider.DefaultIdempotencyKeyLife
		if options.GetIdempotencyKeyTtl() != nil {
			life = options.GetIdempotencyKeyTtl().AsDuration()
		}
		idempotency = NewRecordNamingRun(provider.RecordIdempotency, deployed.topicName, key, execution, now.Add(life))
		held, created, err := e.cfg.Store.EnsureRecord(ctx, idempotency)
		if err != nil {
			return "", err
		}
		if !created {
			return ReadRecordedRun(held)
		}
	}
	forget := func(cause error) error {
		if idempotency.Key == "" {
			return cause
		}
		return errors.Join(cause, e.cfg.Store.DeleteRecord(context.WithoutCancel(ctx), idempotency))
	}
	run := newTaskRun(deployed, messageID, now, req)
	if debounce != nil {
		return e.triggerDebounced(ctx, deployed, debounce.GetKey(), run, idempotency, forget)
	}
	if err := e.start(ctx, run); err != nil {
		return "", forget(err)
	}
	return execution, nil
}

func (e *Engine) triggerDebounced(ctx context.Context, deployed deployedConsumer, key string, run Run, idempotency provider.ExpiringRecord, forget func(error) error) (string, error) {
	run, err := e.create(ctx, run)
	if err != nil {
		return "", forget(err)
	}
	pending, err := e.debounce(ctx, deployed, key, run.Execution, run.DueAt)
	if err == nil && pending == "" {
		if err := e.send(ctx, run); err != nil {
			return "", forget(err)
		}
		return run.Execution, nil
	}
	if dropErr := e.cfg.Store.DeleteRun(context.WithoutCancel(ctx), run); dropErr != nil {
		slog.Warn("delete the run a debounced trigger folded into another", "execution", run.Execution, "error", dropErr)
	}
	if err != nil {
		return "", forget(err)
	}
	if idempotency.Key != "" {
		pointed := NewRecordNamingRun(idempotency.Purpose, idempotency.Topic, idempotency.Key, pending, idempotency.ExpiresAt)
		if err := e.cfg.Store.ReplaceRecord(ctx, pointed, idempotency.Value); err != nil && !errors.Is(err, keyvalue.ErrStale) {
			return "", err
		}
	}
	return pending, nil
}

func newTaskRun(deployed deployedConsumer, messageID string, now time.Time, req runRequest) Run {
	run := Run{
		Execution:   ExecutionOf(messageID, deployed.consumer.Name),
		Topic:       deployed.topicName,
		Consumer:    deployed.consumer.Name,
		Status:      provider.RunQueued,
		Payload:     req.payload,
		MaxAttempts: AttemptsFor(deployed.consumer.Retry, req.maxAttempts),
		Tags:        req.tags,
		Metadata:    req.metadata,
		Message:     messageID,
		PublishedAt: now,
		Key:         req.key,
		Lane:        LaneFor(deployed.consumer.Lanes, req.lane),
		Delivery:    NewMessageID(now),
		CreatedAt:   now,
		DueAt:       req.dueAt,
	}
	if req.dueAt.After(now) {
		run.Status = provider.RunDelayed
	}
	if req.ttl > 0 {
		run.ExpiresAt = req.dueAt.Add(req.ttl)
	}
	return run
}

func messageOf(run Run) Message {
	return Message{
		Topic:       run.Topic,
		Consumer:    run.Consumer,
		Execution:   run.Execution,
		Delivery:    run.Delivery,
		ID:          run.Message,
		PublishedAt: run.PublishedAt,
		DueAt:       run.DueAt,
		Key:         run.Key,
		Lane:        run.Lane,
	}
}

func (e *Engine) start(ctx context.Context, run Run) error {
	run, err := e.create(ctx, run)
	if err != nil {
		return err
	}
	return e.send(ctx, run)
}

func (e *Engine) create(ctx context.Context, run Run) (Run, error) {
	run = run.truncated()
	revision, err := e.cfg.Store.CreateRun(ctx, run)
	if err != nil {
		return Run{}, fmt.Errorf("record run %s: %w", run.Execution, err)
	}
	run.Revision = revision
	return run, nil
}

func (e *Engine) send(ctx context.Context, run Run) error {
	if err := e.cfg.Queue.Send(ctx, messageOf(run)); err != nil {
		return errors.Join(err, e.cfg.Store.DeleteRun(context.WithoutCancel(ctx), run))
	}
	return nil
}

func (e *Engine) debounce(ctx context.Context, deployed deployedConsumer, key, execution string, due time.Time) (string, error) {
	record := NewRecordNamingRun(provider.RecordDebounce, deployed.topicName, key, execution, due)
	for range maxRaces {
		held, created, err := e.cfg.Store.EnsureRecord(ctx, record)
		if err != nil || created {
			return "", err
		}
		pending, err := ReadRecordedRun(held)
		if err != nil {
			return "", err
		}
		_, err = e.reschedule(ctx, deployed, pending, due, true)
		switch code := connect.CodeOf(err); {
		case err == nil:
			refreshed := NewRecordNamingRun(record.Purpose, record.Topic, record.Key, pending, due)
			if err := e.cfg.Store.ReplaceRecord(ctx, refreshed, held.Value); err != nil && !errors.Is(err, keyvalue.ErrStale) {
				return "", err
			}
			return pending, nil
		case code == connect.CodeFailedPrecondition || code == connect.CodeNotFound:
			if err := e.cfg.Store.ReplaceRecord(ctx, record, held.Value); !errors.Is(err, keyvalue.ErrStale) {
				return "", err
			}
		default:
			return "", err
		}
	}
	return "", connect.NewError(connect.CodeAborted, fmt.Errorf("the debounce key %q of %s changed under each of %d tries to take it", key, deployed.topicName, maxRaces))
}

func refuseMissingRun(id string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("no run %q", id))
}

func refuseGone(id string, err error) error {
	if errors.Is(err, errRunGone) {
		return refuseMissingRun(id)
	}
	return err
}

func (e *Engine) readTaskRun(ctx context.Context, id string) (Run, error) {
	task, ok := taskOf(id)
	if !ok {
		return Run{}, refuseMissingRun(id)
	}
	run, err := e.cfg.Store.ReadRun(ctx, task, id)
	if errors.Is(err, keyvalue.ErrNotFound) {
		return Run{}, refuseMissingRun(id)
	}
	return run, err
}

func (e *Engine) reschedule(ctx context.Context, deployed deployedConsumer, execution string, due time.Time, shiftExpiry bool) (Run, error) {
	run, err := e.cfg.Store.ReadRun(ctx, deployed.topicName, execution)
	if errors.Is(err, keyvalue.ErrNotFound) {
		return Run{}, refuseMissingRun(execution)
	}
	if err != nil {
		return Run{}, err
	}
	for range maxRaces {
		now := time.Now()
		if status := statusAt(run, now, true); status != provider.RunDelayed {
			return Run{}, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %q is %s, and only a delayed run can be rescheduled", execution, status))
		}
		moved := run
		moved.DueAt, moved.Delivery, moved.Held, moved.Status = due, NewMessageID(now), "", provider.RunDelayed
		if !due.After(now) {
			moved.Status = provider.RunQueued
		}
		if shiftExpiry && !run.ExpiresAt.IsZero() {
			moved.ExpiresAt = run.ExpiresAt.Add(due.Sub(run.DueAt))
		}
		written, err := e.write(ctx, moved)
		if errors.Is(err, keyvalue.ErrStale) {
			if run, err = e.reread(ctx, run); err != nil {
				return Run{}, refuseGone(execution, err)
			}
			continue
		}
		if err != nil {
			return Run{}, err
		}
		if err := e.cfg.Queue.Send(ctx, messageOf(written)); err != nil {
			restored := written
			restored.DueAt, restored.Delivery, restored.Held, restored.Status, restored.ExpiresAt = run.DueAt, run.Delivery, run.Held, run.Status, run.ExpiresAt
			_, restoreErr := e.write(context.WithoutCancel(ctx), restored)
			return Run{}, errors.Join(err, restoreErr)
		}
		e.release(ctx, run)
		return written, nil
	}
	return Run{}, connect.NewError(connect.CodeAborted, fmt.Errorf("run %q changed under each of %d tries to reschedule it", execution, maxRaces))
}

func (e *Engine) release(ctx context.Context, run Run) {
	if run.Held == "" {
		return
	}
	if err := e.cfg.Queue.Release(context.WithoutCancel(ctx), run.Topic, run.Consumer, run.Held); err != nil {
		slog.Warn("release the message a run held on its queue", "execution", run.Execution, "error", err)
	}
}

var messageIDLength = len(NewMessageID(time.Time{}))

func taskOf(id string) (string, bool) {
	if len(id) <= messageIDLength+1 || id[messageIDLength] != '-' {
		return "", false
	}
	return id[messageIDLength+1:], true
}
