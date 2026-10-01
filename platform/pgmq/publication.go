package pgmq

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ocelhq/ocel/pkg/keyvalue"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
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

func executionOf(messageID, consumer string) string { return messageID + "-" + consumer }

func laneName(lane topicv1.Lane) string {
	switch lane {
	case topicv1.Lane_LANE_HIGH:
		return "high"
	case topicv1.Lane_LANE_LOW:
		return "low"
	default:
		return "default"
	}
}

func (e *Engine) publish(ctx context.Context, tx pgx.Tx, p publication) ([]string, error) {
	task := isTask(p.topic)
	if !task {
		staged := provider.ExpiringRecord{Purpose: provider.RecordStagedPayload, Topic: p.topicName, Key: p.messageID, Value: p.payload, ExpiresAt: p.dueAt.Add(stagedPayloadLife)}
		if _, _, err := ensureRecord(ctx, tx, staged); err != nil {
			return nil, err
		}
	}
	var executions []string
	for _, consumer := range p.topic.GetConsumers() {
		ref := consumerRef{topicName: p.topicName, topic: p.topic, consumer: consumer, queue: queueName(p.topicName, consumer.GetName())}
		execution := executionOf(p.messageID, consumer.GetName())
		run := enqueuedRun{
			execution:   execution,
			ref:         ref,
			messageID:   p.messageID,
			publishedAt: p.publishedAt,
			dueAt:       p.dueAt,
			key:         p.key,
			lane:        laneFor(consumer, p.lane),
			maxAttempts: retryPolicyOf(ref).attemptsFor(p.maxAttempts),
			tags:        p.tags,
			metadata:    p.metadata,
		}
		if task {
			run.payload = p.payload
		}
		if p.ttl > 0 {
			run.expiresAt = p.dueAt.Add(p.ttl)
		}
		if err := insertRun(ctx, tx, run); err != nil {
			return nil, err
		}
		executions = append(executions, execution)
	}
	return executions, nil
}

func laneFor(consumer *contractv1.ManifestConsumer, lane topicv1.Lane) string {
	lanes := consumer.GetLanes()
	if lane == topicv1.Lane_LANE_UNSPECIFIED || (len(lanes) > 0 && !slices.Contains(lanes, lane)) {
		return laneName(topicv1.Lane_LANE_DEFAULT)
	}
	return laneName(lane)
}

type enqueuedRun struct {
	execution   string
	ref         consumerRef
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
	msgID, err := sendToQueue(ctx, tx, run.ref, run.execution, run.key, run.lane, run.dueAt)
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
		run.execution, run.ref.topicName, run.ref.consumer.GetName(), string(status), jsonOrNull(run.payload), orEmpty(run.tags),
		jsonOrNull(run.metadata), run.publishedAt, run.dueAt, nullTime(run.expiresAt), string(revision), run.messageID,
		run.ref.queue, msgID, run.key, run.lane, run.maxAttempts,
	); err != nil {
		return fmt.Errorf("record run %s: %w", run.execution, err)
	}
	return nil
}

func sendToQueue(ctx context.Context, tx pgx.Tx, ref consumerRef, execution, key, lane string, due time.Time) (int64, error) {
	body, err := json.Marshal(queueMessage{Execution: execution, Lane: lane})
	if err != nil {
		return 0, err
	}
	var headers any
	if ref.topic.GetOrdered() {
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
	if err := tx.QueryRow(ctx, "SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::timestamptz)", ref.queue, string(body), headers, due).Scan(&msgID); err != nil {
		return 0, fmt.Errorf("send %s to queue %s: %w", execution, ref.queue, err)
	}
	return msgID, nil
}
