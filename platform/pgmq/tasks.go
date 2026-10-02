package pgmq

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	"github.com/ocelhq/ocel/pkg/envelope"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

type Tasks struct {
	engine *Engine
}

func (e *Engine) Tasks() Tasks { return Tasks{engine: e} }

func (t Tasks) task(name string) (*contractv1.ManifestTopic, error) {
	topic, found := t.engine.current().Topics[name]
	if !found || !runs.IsTask(topic) {
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
	if err := runs.RefuseNonJSON(payload); err != nil {
		return "", err
	}
	toPublish := publication{
		topicName:   name,
		topic:       topic,
		messageID:   envelope.NewMessageID(now),
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
	if metadata := options.GetMetadata(); len(metadata) > 0 {
		if err := runs.RefuseNonObject(metadata); err != nil {
			return "", err
		}
		toPublish.metadata = metadata
	}
	execution := runs.ExecutionOf(toPublish.messageID, topic.GetConsumers()[0].GetName())
	var idempotency provider.ExpiringRecord
	if key := options.GetIdempotencyKey(); key != "" {
		life := provider.DefaultIdempotencyKeyLife
		if options.GetIdempotencyKeyTtl() != nil {
			life = options.GetIdempotencyKeyTtl().AsDuration()
		}
		idempotency = runs.NewRecordNamingRun(provider.RecordIdempotency, name, key, execution, now.Add(life))
		existing, created, err := ensureRecord(ctx, tx, idempotency)
		if err != nil {
			return "", err
		}
		if !created {
			return runs.ReadRecordedRun(existing)
		}
	}
	if debounce := options.GetDebounce(); debounce != nil {
		toPublish.dueAt = now.Add(debounce.GetDelay().AsDuration())
		pending, err := t.debounce(ctx, tx, runs.NewRecordNamingRun(provider.RecordDebounce, name, debounce.GetKey(), execution, toPublish.dueAt))
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

func pointIdempotencyAt(ctx context.Context, tx pgx.Tx, idempotency provider.ExpiringRecord, execution string) error {
	if idempotency.Key == "" {
		return nil
	}
	pointed := runs.NewRecordNamingRun(idempotency.Purpose, idempotency.Topic, idempotency.Key, execution, idempotency.ExpiresAt)
	_, err := tx.Exec(ctx, `UPDATE ocel.records SET value = $4 WHERE purpose = $1 AND topic = $2 AND key = $3`,
		string(pointed.Purpose), pointed.Topic, pointed.Key, string(pointed.Value))
	return err
}

func (t Tasks) debounce(ctx context.Context, tx pgx.Tx, record provider.ExpiringRecord) (string, error) {
	existing, created, err := ensureRecord(ctx, tx, record)
	if err != nil || created {
		return "", err
	}
	pending, err := runs.ReadRecordedRun(existing)
	if err != nil {
		return "", err
	}
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

func (t Tasks) RetrieveRun(ctx context.Context, req *taskv1.RetrieveRunRequest) (*taskv1.RetrieveRunResponse, error) {
	run, err := readRun(ctx, t.engine.pool, req.GetId())
	if errors.Is(err, keyvalue.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no run %q", req.GetId()))
	}
	if err != nil {
		return nil, err
	}
	return &taskv1.RetrieveRunResponse{Run: runs.NewRunMessage(run)}, nil
}
