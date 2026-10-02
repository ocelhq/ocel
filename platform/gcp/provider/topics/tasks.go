package topics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/envelope"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

type Tasks struct {
	deployment Deployment
}

func (t Tasks) declared(name string) (*provider.TopicSpec, error) {
	topic, found := t.deployment.Declared[name]
	if !found || !runs.IsTask(topic) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no task named %q is deployed", name))
	}
	return topic, nil
}

func (t Tasks) Trigger(ctx context.Context, req *taskv1.TriggerRequest) (*taskv1.TriggerResponse, error) {
	topic, err := t.declared(req.GetTask())
	if err != nil {
		return nil, err
	}
	id, err := t.trigger(ctx, req.GetTask(), topic, req.GetPayload(), req.GetOptions())
	if err != nil {
		return nil, err
	}
	return &taskv1.TriggerResponse{Id: id}, nil
}

func refuseInvalidTrigger(payload []byte, options *taskv1.TriggerOptions) error {
	if err := runs.RefuseNonJSON(payload); err != nil {
		return err
	}
	if metadata := options.GetMetadata(); len(metadata) > 0 {
		return runs.RefuseNonObject(metadata)
	}
	return nil
}

func (t Tasks) trigger(ctx context.Context, name string, topic *provider.TopicSpec, payload []byte, options *taskv1.TriggerOptions) (string, error) {
	if err := refuseInvalidTrigger(payload, options); err != nil {
		return "", err
	}
	metadata := options.GetMetadata()
	now := time.Now()
	toPublish := publication{
		topicName:   name,
		topic:       topic,
		messageID:   envelope.NewMessageID(now),
		publishedAt: now,
		dueAt:       now,
		payload:     payload,
		key:         options.GetKey(),
		lane:        string(runs.LaneOf(options.GetLane())),
		maxAttempts: options.GetMaxAttempts(),
	}
	if options.GetDueAt() != nil {
		toPublish.dueAt = options.GetDueAt().AsTime()
	}
	if debounce := options.GetDebounce(); debounce != nil {
		toPublish.dueAt = now.Add(debounce.GetDelay().AsDuration())
	}
	execution := runs.ExecutionOf(toPublish.messageID, topic.Consumers[0].Name)
	var idempotency provider.ExpiringRecord
	if key := options.GetIdempotencyKey(); key != "" {
		life := provider.DefaultIdempotencyKeyLife
		if options.GetIdempotencyKeyTtl() != nil {
			life = options.GetIdempotencyKeyTtl().AsDuration()
		}
		idempotency = runs.NewRecordNamingRun(provider.RecordIdempotency, name, key, execution, now.Add(life))
		existing, created, err := t.deployment.Store().EnsureRecord(ctx, idempotency)
		if err != nil {
			return "", err
		}
		if !created {
			return runs.ReadRecordedRun(existing)
		}
	}
	ttl := topic.TTL
	if options.GetTtl() != nil {
		ttl = options.GetTtl().AsDuration()
	}
	run := t.deployment.newStoredRun(toPublish, now, ttl, options.GetTags(), metadata)
	store := t.deployment.Store()
	if debounce := options.GetDebounce(); debounce != nil {
		debounced := runs.NewRecordNamingRun(provider.RecordDebounce, name, debounce.GetKey(), execution, toPublish.dueAt)
		pending, err := t.debounce(ctx, debounced, run)
		if err != nil {
			return "", errors.Join(err, store.deleteRecordsHolding(ctx, idempotency))
		}
		if pending != execution {
			return pending, store.pointRecordAt(ctx, idempotency, pending)
		}
		if err := t.deployment.publishRun(ctx, toPublish, run); err != nil {
			return "", errors.Join(err, store.deleteRecordsHolding(ctx, idempotency, debounced))
		}
		return execution, nil
	}
	if err := t.start(ctx, toPublish, run); err != nil {
		return "", errors.Join(err, store.deleteRecordsHolding(ctx, idempotency))
	}
	return execution, nil
}

func (d Deployment) newStoredRun(toPublish publication, now time.Time, ttl time.Duration, tags []string, metadata []byte) storedRun {
	consumer := toPublish.topic.Consumers[0]
	delayTask := d.delayTaskOf(toPublish)
	status := provider.RunQueued
	if delayTask != "" {
		status = provider.RunDelayed
	}
	run := storedRun{
		Run: provider.Run{
			Execution: runs.ExecutionOf(toPublish.messageID, consumer.Name),
			Topic:     toPublish.topicName,
			Consumer:  consumer.Name,
			Status:    status,
			Payload:   toPublish.payload,
			Tags:      tags,
			Metadata:  metadata,
			CreatedAt: now,
			DueAt:     toPublish.dueAt,
		},
		delivery: deliveryFields{
			MessageID:   toPublish.messageID,
			PublishedAt: &now,
			MaxAttempts: runs.AttemptsFor(consumer.Retry, toPublish.maxAttempts),
			Key:         toPublish.key,
			Lane:        toPublish.lane,
			DelayTask:   delayTask,
		},
	}
	if ttl > 0 {
		run.ExpiresAt = toPublish.dueAt.Add(ttl)
	}
	return run
}

func (t Tasks) start(ctx context.Context, toPublish publication, run storedRun) error {
	if err := t.deployment.Store().createRun(ctx, run); err != nil {
		return err
	}
	return t.deployment.publishRun(ctx, toPublish, run)
}

func (d Deployment) publishRun(ctx context.Context, toPublish publication, run storedRun) error {
	if err := d.publish(ctx, toPublish, run.delivery.DelayTask); err != nil {
		return d.failUnpublished(ctx, run.Execution, err)
	}
	return nil
}

func (t Tasks) debounce(ctx context.Context, record provider.ExpiringRecord, run storedRun) (string, error) {
	store := t.deployment.Store()
	var err error
	for range debounceAttempts {
		var pending string
		pending, err = store.ensureDebouncedRun(ctx, record, run)
		if err != nil || pending == run.Execution {
			return pending, err
		}
		err = t.move(ctx, pending, record.ExpiresAt, true)
		if err == nil {
			return pending, store.extendRecordHolding(ctx, runs.NewRecordNamingRun(record.Purpose, record.Topic, record.Key, pending, record.ExpiresAt))
		}
		if code := connect.CodeOf(err); code != connect.CodeFailedPrecondition && code != connect.CodeNotFound {
			return "", err
		}
	}
	return "", err
}

func (d Deployment) failUnpublished(ctx context.Context, execution string, cause error) error {
	_, err := d.Store().changeRun(ctx, execution, func(run *storedRun, found bool) error {
		if !found || !runs.IsUnfinished(run.Status) {
			return errRunUnchanged
		}
		run.Status, run.Error, run.FinishedAt = provider.RunFailed, cause.Error(), time.Now()
		return nil
	})
	if err != nil && !errors.Is(err, errRunUnchanged) {
		return errors.Join(cause, err)
	}
	return cause
}

func (t Tasks) BatchTrigger(ctx context.Context, req *taskv1.BatchTriggerRequest) (*taskv1.BatchTriggerResponse, error) {
	topic, err := t.declared(req.GetTask())
	if err != nil {
		return nil, err
	}
	for i, item := range req.GetItems() {
		if err := refuseInvalidTrigger(item.GetPayload(), item.GetOptions()); err != nil {
			return nil, connect.NewError(connect.CodeOf(err), fmt.Errorf("item %d: %w", i, err))
		}
	}
	ids := make([]string, 0, len(req.GetItems()))
	for i, item := range req.GetItems() {
		id, err := t.trigger(ctx, req.GetTask(), topic, item.GetPayload(), item.GetOptions())
		if err != nil {
			return &taskv1.BatchTriggerResponse{Ids: ids}, connect.NewError(connect.CodeOf(err), fmt.Errorf("item %d failed after runs %v were triggered: %w", i, ids, err))
		}
		ids = append(ids, id)
	}
	return &taskv1.BatchTriggerResponse{Ids: ids}, nil
}

func (t Tasks) ListRuns(ctx context.Context, req *taskv1.ListRunsRequest) (*taskv1.ListRunsResponse, error) {
	filter := provider.RunFilter{Topic: req.GetTask(), Statuses: runs.StatusesOf(req.GetStatuses()), Tags: req.GetTags(), Cursor: req.GetCursor(), Limit: int(req.GetLimit())}
	page, err := t.deployment.Store().ListRuns(ctx, filter)
	if errors.Is(err, runs.ErrUnknownCursor) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	resp := &taskv1.ListRunsResponse{NextCursor: page.NextCursor}
	for _, run := range page.Runs {
		resp.Runs = append(resp.Runs, newRunMessageAt(run, now))
	}
	return resp, nil
}

func (t Tasks) RetrieveRun(ctx context.Context, req *taskv1.RetrieveRunRequest) (*taskv1.RetrieveRunResponse, error) {
	run, err := t.deployment.Store().ReadRun(ctx, req.GetId())
	if errors.Is(err, keyvalue.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no run %q", req.GetId()))
	}
	if err != nil {
		return nil, err
	}
	return &taskv1.RetrieveRunResponse{Run: newRunMessageAt(run, time.Now())}, nil
}

func newRunMessageAt(run provider.Run, now time.Time) *taskv1.Run {
	run.Status = statusAt(run, now)
	return runs.NewRunMessage(run)
}

func statusAt(run provider.Run, now time.Time) provider.RunStatus {
	switch {
	case runs.IsWaiting(run.Status) && run.Attempts == 0 && !run.ExpiresAt.IsZero() && !run.ExpiresAt.After(now):
		return provider.RunExpired
	case run.Status == provider.RunDelayed && !run.DueAt.After(now):
		return provider.RunQueued
	}
	return run.Status
}
