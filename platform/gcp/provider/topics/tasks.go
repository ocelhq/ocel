package topics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/envelope"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Tasks struct {
	deployment Deployment
}

type recordedRun struct {
	Run string `json:"run"`
}

func (t Tasks) task(name string) (*contractv1.ManifestTopic, error) {
	topic, found := t.deployment.Declared[name]
	if !found || !isTask(topic) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no task named %q is deployed", name))
	}
	return topic, nil
}

func (t Tasks) Trigger(ctx context.Context, req *taskv1.TriggerRequest) (*taskv1.TriggerResponse, error) {
	topic, err := t.task(req.GetTask())
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
	if err := refuseNonJSON(payload); err != nil {
		return err
	}
	if metadata := options.GetMetadata(); len(metadata) > 0 {
		return refuseNonObject(metadata)
	}
	return nil
}

func (t Tasks) trigger(ctx context.Context, name string, topic *contractv1.ManifestTopic, payload []byte, options *taskv1.TriggerOptions) (string, error) {
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
		lane:        laneName(options.GetLane()),
		maxAttempts: options.GetMaxAttempts(),
	}
	if options.GetDueAt() != nil {
		toPublish.dueAt = options.GetDueAt().AsTime()
	}
	if debounce := options.GetDebounce(); debounce != nil {
		toPublish.dueAt = now.Add(debounce.GetDelay().AsDuration())
	}
	execution := executionOf(toPublish.messageID, topic.GetConsumers()[0].GetName())
	var idempotency provider.ExpiringRecord
	if key := options.GetIdempotencyKey(); key != "" {
		life := provider.DefaultIdempotencyKeyLife
		if options.GetIdempotencyKeyTtl() != nil {
			life = options.GetIdempotencyKeyTtl().AsDuration()
		}
		idempotency = newRunRecord(provider.RecordIdempotency, name, key, execution, now.Add(life))
		existing, created, err := t.deployment.Store().EnsureRecord(ctx, idempotency)
		if err != nil {
			return "", err
		}
		if !created {
			return readRecordedRun(existing)
		}
	}
	ttl := topic.GetTtl().AsDuration()
	if options.GetTtl() != nil {
		ttl = options.GetTtl().AsDuration()
	}
	run := t.deployment.newStoredRun(toPublish, now, ttl, options.GetTags(), metadata)
	store := t.deployment.Store()
	if debounce := options.GetDebounce(); debounce != nil {
		debounced := newRunRecord(provider.RecordDebounce, name, debounce.GetKey(), execution, toPublish.dueAt)
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

func (d Deployment) newStoredRun(toPublish publication, now time.Time, ttl time.Duration, tags []string, metadata []byte) runRecord {
	consumer := toPublish.topic.GetConsumers()[0]
	delayTask := d.delayTaskOf(toPublish)
	status := provider.RunQueued
	if delayTask != "" {
		status = provider.RunDelayed
	}
	run := runRecord{
		Run: provider.Run{
			Execution: executionOf(toPublish.messageID, consumer.GetName()),
			Topic:     toPublish.topicName,
			Consumer:  consumer.GetName(),
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
			MaxAttempts: retryPolicyOf(toPublish.topic, consumer).attemptsFor(toPublish.maxAttempts),
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

func (t Tasks) start(ctx context.Context, toPublish publication, run runRecord) error {
	if err := t.deployment.Store().createRun(ctx, run); err != nil {
		return err
	}
	return t.deployment.publishRun(ctx, toPublish, run)
}

func (d Deployment) publishRun(ctx context.Context, toPublish publication, run runRecord) error {
	if err := d.publish(ctx, toPublish, run.delivery.DelayTask); err != nil {
		return d.failUnpublished(ctx, run.Execution, err)
	}
	return nil
}

func (t Tasks) debounce(ctx context.Context, record provider.ExpiringRecord, run runRecord) (string, error) {
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
			return pending, store.extendRecordHolding(ctx, newRunRecord(record.Purpose, record.Topic, record.Key, pending, record.ExpiresAt))
		}
		if code := connect.CodeOf(err); code != connect.CodeFailedPrecondition && code != connect.CodeNotFound {
			return "", err
		}
	}
	return "", err
}

func (d Deployment) failUnpublished(ctx context.Context, execution string, cause error) error {
	_, err := d.Store().changeRun(ctx, execution, func(record *runRecord, found bool) error {
		if !found || isFinished(record.Status) {
			return errRunUnchanged
		}
		record.Status, record.Error, record.FinishedAt = provider.RunFailed, cause.Error(), time.Now()
		return nil
	})
	if err != nil && !errors.Is(err, errRunUnchanged) {
		return errors.Join(cause, err)
	}
	return cause
}

func newRunRecord(purpose provider.RecordPurpose, task, key, execution string, expires time.Time) provider.ExpiringRecord {
	value, _ := json.Marshal(recordedRun{Run: execution})
	return provider.ExpiringRecord{Purpose: purpose, Topic: task, Key: key, Value: value, ExpiresAt: expires}
}

func readRecordedRun(record provider.ExpiringRecord) (string, error) {
	var recorded recordedRun
	if err := json.Unmarshal(record.Value, &recorded); err != nil {
		return "", fmt.Errorf("read the run the %s record %q of %s names: %w", record.Purpose, record.Key, record.Topic, err)
	}
	return recorded.Run, nil
}

func refuseNonObject(metadata []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &object); err != nil || object == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the metadata is not a JSON object"))
	}
	return nil
}

func (t Tasks) BatchTrigger(ctx context.Context, req *taskv1.BatchTriggerRequest) (*taskv1.BatchTriggerResponse, error) {
	topic, err := t.task(req.GetTask())
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
	filter := provider.RunFilter{Topic: req.GetTask(), Tags: req.GetTags(), Cursor: req.GetCursor(), Limit: int(req.GetLimit())}
	for _, status := range req.GetStatuses() {
		for stored, wire := range runStatuses {
			if wire == status {
				filter.Statuses = append(filter.Statuses, stored)
			}
		}
	}
	page, err := t.deployment.Store().ListRuns(ctx, filter)
	if errors.Is(err, ErrUnknownCursor) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	resp := &taskv1.ListRunsResponse{NextCursor: page.NextCursor}
	for _, run := range page.Runs {
		resp.Runs = append(resp.Runs, newRunMessage(run, now))
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
	return &taskv1.RetrieveRunResponse{Run: newRunMessage(run, time.Now())}, nil
}

var runStatuses = map[provider.RunStatus]taskv1.RunStatus{
	provider.RunDelayed:   taskv1.RunStatus_RUN_STATUS_DELAYED,
	provider.RunQueued:    taskv1.RunStatus_RUN_STATUS_QUEUED,
	provider.RunExecuting: taskv1.RunStatus_RUN_STATUS_EXECUTING,
	provider.RunCompleted: taskv1.RunStatus_RUN_STATUS_COMPLETED,
	provider.RunFailed:    taskv1.RunStatus_RUN_STATUS_FAILED,
	provider.RunCanceled:  taskv1.RunStatus_RUN_STATUS_CANCELED,
	provider.RunExpired:   taskv1.RunStatus_RUN_STATUS_EXPIRED,
	provider.RunTimedOut:  taskv1.RunStatus_RUN_STATUS_TIMED_OUT,
}

func newRunMessage(run provider.Run, now time.Time) *taskv1.Run {
	return &taskv1.Run{
		Id:         run.Execution,
		Task:       run.Topic,
		Status:     runStatuses[statusAt(run, now)],
		Payload:    run.Payload,
		Output:     run.Output,
		Metadata:   run.Metadata,
		Error:      run.Error,
		Attempts:   int32(run.Attempts),
		Tags:       run.Tags,
		CreatedAt:  timestampOf(run.CreatedAt),
		DueAt:      timestampOf(run.DueAt),
		StartedAt:  timestampOf(run.StartedAt),
		FinishedAt: timestampOf(run.FinishedAt),
		ExpiresAt:  timestampOf(run.ExpiresAt),
	}
}

func statusAt(run provider.Run, now time.Time) provider.RunStatus {
	unstarted := run.Status == provider.RunQueued || run.Status == provider.RunDelayed
	switch {
	case unstarted && run.Attempts == 0 && !run.ExpiresAt.IsZero() && !run.ExpiresAt.After(now):
		return provider.RunExpired
	case run.Status == provider.RunDelayed && !run.DueAt.After(now):
		return provider.RunQueued
	}
	return run.Status
}

func timestampOf(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
