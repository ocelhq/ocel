package pgmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Tasks struct {
	engine *Engine
}

func (e *Engine) Tasks() Tasks { return Tasks{engine: e} }

func (t Tasks) task(name string) (*contractv1.ManifestTopic, error) {
	topic, found := t.engine.current().Topics[name]
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
	var id string
	err = t.engine.inTx(ctx, func(tx pgx.Tx) error {
		id, err = t.trigger(ctx, tx, req.GetTask(), topic, req.GetPayload(), req.GetOptions())
		return err
	})
	if err != nil {
		return nil, err
	}
	t.engine.signalTopic(req.GetTask(), topic)
	return &taskv1.TriggerResponse{Id: id}, nil
}

func (t Tasks) trigger(ctx context.Context, tx pgx.Tx, name string, topic *contractv1.ManifestTopic, payload []byte, options *taskv1.TriggerOptions) (string, error) {
	now := time.Now()
	if err := refuseNonJSON(payload); err != nil {
		return "", err
	}
	toPublish := publication{
		topicName:   name,
		topic:       topic,
		messageID:   newMessageID(now),
		publishedAt: now,
		dueAt:       now,
		payload:     payload,
		key:         options.GetKey(),
		lane:        options.GetLane(),
		maxAttempts: options.GetMaxAttempts(),
		tags:        options.GetTags(),
		ttl:         topic.GetTtl().AsDuration(),
	}
	if options.GetDueAt() != nil {
		toPublish.dueAt = options.GetDueAt().AsTime()
	}
	if options.GetTtl() != nil {
		toPublish.ttl = options.GetTtl().AsDuration()
	}
	if options.GetMetadata() != nil {
		metadata, err := protojson.Marshal(options.GetMetadata())
		if err != nil {
			return "", err
		}
		toPublish.metadata = metadata
	}
	execution := executionOf(toPublish.messageID, topic.GetConsumers()[0].GetName())
	var idempotency provider.ExpiringRecord
	if key := options.GetIdempotencyKey(); key != "" {
		life := provider.DefaultIdempotencyKeyLife
		if options.GetIdempotencyKeyTtl() != nil {
			life = options.GetIdempotencyKeyTtl().AsDuration()
		}
		idempotency = newRunRecord(provider.RecordIdempotency, name, key, execution, now.Add(life))
		existing, created, err := ensureRecord(ctx, tx, idempotency)
		if err != nil || !created {
			return readRecordedRun(existing), err
		}
	}
	if debounce := options.GetDebounce(); debounce != nil {
		toPublish.dueAt = now.Add(debounce.GetDelay().AsDuration())
		pending, err := t.debounce(ctx, tx, newRunRecord(provider.RecordDebounce, name, debounce.GetKey(), execution, toPublish.dueAt))
		if err != nil {
			return "", err
		}
		if pending != "" {
			return pending, pointIdempotencyAt(ctx, tx, idempotency, pending)
		}
	}
	if _, err := t.engine.publish(ctx, tx, toPublish); err != nil {
		return "", err
	}
	return execution, nil
}

type recordedRun struct {
	Run string `json:"run"`
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

func pointIdempotencyAt(ctx context.Context, tx pgx.Tx, idempotency provider.ExpiringRecord, execution string) error {
	if idempotency.Key == "" {
		return nil
	}
	pointed := newRunRecord(idempotency.Purpose, idempotency.Topic, idempotency.Key, execution, idempotency.ExpiresAt)
	_, err := tx.Exec(ctx, `UPDATE ocel.records SET value = $4 WHERE purpose = $1 AND topic = $2 AND key = $3`,
		string(pointed.Purpose), pointed.Topic, pointed.Key, string(pointed.Value))
	return err
}

func (t Tasks) debounce(ctx context.Context, tx pgx.Tx, record provider.ExpiringRecord) (string, error) {
	existing, created, err := ensureRecord(ctx, tx, record)
	if err != nil || created {
		return "", err
	}
	pending := readRecordedRun(existing)
	run, err := lockRun(ctx, tx, pending)
	if err != nil && connect.CodeOf(err) != connect.CodeNotFound {
		return "", err
	}
	if err == nil && run.status == provider.RunDelayed && run.message != nil {
		if _, err := tx.Exec(ctx, `UPDATE ocel.runs SET due_at = $2, expires_at = expires_at + ($2 - due_at), revision = `+newRevisionSQL+` WHERE execution = $1`, pending, record.ExpiresAt); err != nil {
			return "", err
		}
		if _, err := tx.Exec(ctx, "SELECT pgmq.set_vt($1, $2::bigint, $3::timestamptz)", run.queue, *run.message, record.ExpiresAt); err != nil {
			return "", err
		}
		_, err := tx.Exec(ctx, `UPDATE ocel.records SET expires_at = $4 WHERE purpose = $1 AND topic = $2 AND key = $3`,
			string(record.Purpose), record.Topic, record.Key, record.ExpiresAt)
		return pending, err
	}
	_, err = tx.Exec(ctx, `UPDATE ocel.records SET value = $4, expires_at = $5 WHERE purpose = $1 AND topic = $2 AND key = $3`,
		string(record.Purpose), record.Topic, record.Key, string(record.Value), record.ExpiresAt)
	return "", err
}

func refuseNonJSON(payload []byte) error {
	if len(payload) > 0 && !json.Valid(payload) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the payload is not JSON"))
	}
	return nil
}

func (t Tasks) RetrieveRun(ctx context.Context, req *taskv1.RetrieveRunRequest) (*taskv1.RetrieveRunResponse, error) {
	run, err := readRun(ctx, t.engine.pool, req.GetId())
	if errors.Is(err, keyvalue.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no run %q", req.GetId()))
	}
	if err != nil {
		return nil, err
	}
	return &taskv1.RetrieveRunResponse{Run: newRunMessage(run)}, nil
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

func newRunMessage(run provider.Run) *taskv1.Run {
	message := &taskv1.Run{
		Id:         run.Execution,
		Task:       run.Topic,
		Status:     runStatuses[run.Status],
		Payload:    valueOf(run.Payload),
		Output:     valueOf(run.Output),
		Error:      run.Error,
		Attempts:   int32(run.Attempts),
		Tags:       run.Tags,
		CreatedAt:  timestampOf(run.CreatedAt),
		DueAt:      timestampOf(run.DueAt),
		StartedAt:  timestampOf(run.StartedAt),
		FinishedAt: timestampOf(run.FinishedAt),
		ExpiresAt:  timestampOf(run.ExpiresAt),
	}
	if len(run.Metadata) > 0 {
		metadata := &structpb.Struct{}
		if protojson.Unmarshal(run.Metadata, metadata) == nil {
			message.Metadata = metadata
		}
	}
	return message
}

func valueOf(raw json.RawMessage) *structpb.Value {
	if len(raw) == 0 {
		return nil
	}
	value := &structpb.Value{}
	if err := protojson.Unmarshal(raw, value); err != nil {
		return nil
	}
	return value
}

func timestampOf(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}
