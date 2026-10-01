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

func (t Tasks) trigger(ctx context.Context, name string, topic *contractv1.ManifestTopic, payload []byte, options *taskv1.TriggerOptions) (string, error) {
	if err := refuseNonJSON(payload); err != nil {
		return "", err
	}
	metadata := options.GetMetadata()
	if len(metadata) > 0 {
		if err := refuseNonObject(metadata); err != nil {
			return "", err
		}
	}
	now := time.Now()
	consumer := topic.GetConsumers()[0]
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
	execution := executionOf(toPublish.messageID, consumer.GetName())
	if key := options.GetIdempotencyKey(); key != "" {
		life := provider.DefaultIdempotencyKeyLife
		if options.GetIdempotencyKeyTtl() != nil {
			life = options.GetIdempotencyKeyTtl().AsDuration()
		}
		existing, created, err := t.deployment.Store().EnsureRecord(ctx, newRunRecord(provider.RecordIdempotency, name, key, execution, now.Add(life)))
		if err != nil {
			return "", err
		}
		if !created {
			return readRecordedRun(existing), nil
		}
	}
	ttl := topic.GetTtl().AsDuration()
	if options.GetTtl() != nil {
		ttl = options.GetTtl().AsDuration()
	}
	record := runRecord{
		Run: provider.Run{
			Execution: execution,
			Topic:     name,
			Consumer:  consumer.GetName(),
			Status:    provider.RunQueued,
			Payload:   payload,
			Tags:      options.GetTags(),
			Metadata:  metadata,
			CreatedAt: now,
			DueAt:     toPublish.dueAt,
		},
		delivery: deliveryFields{
			MessageID:   toPublish.messageID,
			PublishedAt: &now,
			MaxAttempts: retryPolicyOf(topic, consumer).attemptsFor(options.GetMaxAttempts()),
			Key:         toPublish.key,
			Lane:        toPublish.lane,
		},
	}
	if ttl > 0 {
		record.ExpiresAt = toPublish.dueAt.Add(ttl)
	}
	if err := t.deployment.Store().createRun(ctx, record); err != nil {
		return "", err
	}
	if err := t.deployment.publishNow(ctx, toPublish); err != nil {
		return "", t.deployment.failUnpublished(ctx, execution, err)
	}
	return execution, nil
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

func readRecordedRun(record provider.ExpiringRecord) string {
	var recorded recordedRun
	_ = json.Unmarshal(record.Value, &recorded)
	return recorded.Run
}

func refuseNonObject(metadata []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &object); err != nil || object == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the metadata is not a JSON object"))
	}
	return nil
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
