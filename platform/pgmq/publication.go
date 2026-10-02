package pgmq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	groupHeader = "x-pgmq-group"

	stagedPayloadLife = 44 * 24 * time.Hour
)

type publication struct {
	topicName   string
	topic       *contractv1.ManifestTopic
	messageID   string
	publishedAt time.Time
	dueAt       time.Time
	payload     json.RawMessage
	key         string
	lane        topicv1.Lane
	maxAttempts int32
	ttl         time.Duration
	tags        []string
	metadata    json.RawMessage
}

type queueMessage struct {
	Execution string `json:"execution"`
	Lane      string `json:"lane"`
}

func (e *Engine) publish(ctx context.Context, tx pgx.Tx, toPublish publication) ([]string, error) {
	task := runs.IsTask(toPublish.topic)
	if !task {
		staged := provider.ExpiringRecord{Purpose: provider.RecordStagedPayload, Topic: toPublish.topicName, Key: toPublish.messageID, Value: toPublish.payload, ExpiresAt: toPublish.dueAt.Add(stagedPayloadLife)}
		if _, _, err := ensureRecord(ctx, tx, staged); err != nil {
			return nil, err
		}
	}
	var executions []string
	for _, consumer := range toPublish.topic.GetConsumers() {
		deployed := deployedConsumer{topicName: toPublish.topicName, topic: toPublish.topic, consumer: consumer, queue: queueName(toPublish.topicName, consumer.GetName())}
		execution := runs.ExecutionOf(toPublish.messageID, consumer.GetName())
		run := enqueuedRun{
			execution:   execution,
			deployed:    deployed,
			messageID:   toPublish.messageID,
			publishedAt: toPublish.publishedAt,
			dueAt:       toPublish.dueAt,
			key:         toPublish.key,
			lane:        laneFor(consumer, toPublish.lane),
			maxAttempts: runs.AttemptsFor(retryPolicyOf(deployed), toPublish.maxAttempts),
			tags:        toPublish.tags,
			metadata:    toPublish.metadata,
		}
		if task {
			run.payload = toPublish.payload
		}
		if toPublish.ttl > 0 {
			run.expiresAt = toPublish.dueAt.Add(toPublish.ttl)
		}
		if err := insertRun(ctx, tx, run); err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, nil
}

func laneFor(consumer *contractv1.ManifestConsumer, lane topicv1.Lane) string {
	reads := make([]provider.Lane, 0, len(consumer.GetLanes()))
	for _, read := range consumer.GetLanes() {
		reads = append(reads, runs.LaneOf(read))
	}
	return string(runs.LaneFor(reads, lane))
}

type enqueuedRun struct {
	execution   string
	deployed    deployedConsumer
	messageID   string
	publishedAt time.Time
	dueAt       time.Time
	payload     json.RawMessage
	key         string
	lane        string
	maxAttempts int
	tags        []string
	metadata    json.RawMessage
	expiresAt   time.Time
}

func insertRun(ctx context.Context, tx pgx.Tx, run enqueuedRun) error {
	msgID, err := sendToQueue(ctx, tx, run.deployed, run.execution, run.key, run.lane, run.dueAt)
	if err != nil {
		return err
	}
	revision, err := keyvalue.NewRevision()
	if err != nil {
		return err
	}
	status := provider.RunQueued
	if run.dueAt.After(time.Now()) {
		status = provider.RunDelayed
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ocel.runs (execution, topic, consumer, status, payload, tags, metadata, created_at, due_at, expires_at,
			revision, message_id, published_at, queue, queue_message, key, lane, max_attempts)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $8, $13, $14, $15, $16, $17)`,
		run.execution, run.deployed.topicName, run.deployed.consumer.GetName(), string(status), jsonOrNull(run.payload), orEmpty(run.tags),
		jsonOrNull(run.metadata), run.publishedAt, run.dueAt, nullTime(run.expiresAt), string(revision), run.messageID,
		run.deployed.queue, msgID, run.key, run.lane, run.maxAttempts,
	); err != nil {
		return fmt.Errorf("record run %s: %w", run.execution, err)
	}
	return nil
}

func sendToQueue(ctx context.Context, tx pgx.Tx, deployed deployedConsumer, execution, key, lane string, due time.Time) (int64, error) {
	body, err := json.Marshal(queueMessage{Execution: execution, Lane: lane})
	if err != nil {
		return 0, err
	}
	var headers any
	if deployed.topic.GetOrdered() {
		group := key
		if group == "" {
			group = execution
		}
		encoded, err := json.Marshal(map[string]string{groupHeader: group})
		if err != nil {
			return 0, err
		}
		headers = string(encoded)
	}
	var msgID int64
	if err := tx.QueryRow(ctx, "SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::timestamptz)", deployed.queue, string(body), headers, due).Scan(&msgID); err != nil {
		return 0, fmt.Errorf("send %s to queue %s: %w", execution, deployed.queue, err)
	}
	return msgID, nil
}
