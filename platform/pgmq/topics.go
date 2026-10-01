package pgmq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const deadLetterStatuses = "('failed', 'timed-out')"

type Topics struct {
	engine *Engine
}

func (e *Engine) Topics() Topics { return Topics{engine: e} }

type recordedMessage struct {
	Message string `json:"message"`
}

func (t Topics) consumerOf(topicName, consumer string) (consumerRef, error) {
	for _, ref := range t.engine.current().consumers() {
		if ref.topicName == topicName && ref.consumer.GetName() == consumer && !isTask(ref.topic) {
			return ref, nil
		}
	}
	return consumerRef{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("topic %q has no consumer %q deployed", topicName, consumer))
}

func (t Topics) Send(ctx context.Context, req *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	topic, found := t.engine.current().Topics[req.GetTopic()]
	if !found || isTask(topic) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no topic named %q is deployed", req.GetTopic()))
	}
	if err := refuseNonJSON(req.GetPayload()); err != nil {
		return nil, err
	}
	now := time.Now()
	p := publication{
		topicName:   req.GetTopic(),
		topic:       topic,
		messageID:   newMessageID(now),
		publishedAt: now,
		dueAt:       now,
		payload:     req.GetPayload(),
		key:         req.GetKey(),
		lane:        req.GetLane(),
	}
	if req.GetDueAt() != nil {
		p.dueAt = req.GetDueAt().AsTime()
	}
	id := p.messageID
	err := t.engine.inTx(ctx, func(tx pgx.Tx) error {
		if key := req.GetIdempotencyKey(); key != "" {
			value, err := json.Marshal(recordedMessage{Message: p.messageID})
			if err != nil {
				return err
			}
			existing, created, err := ensureRecord(ctx, tx, provider.ExpiringRecord{
				Purpose: provider.RecordIdempotency, Topic: req.GetTopic(), Key: key, Value: value, ExpiresAt: now.Add(defaultIdempotencyKeyLife),
			})
			if err != nil {
				return err
			}
			if !created {
				var recorded recordedMessage
				_ = json.Unmarshal(existing.Value, &recorded)
				id = recorded.Message
				return nil
			}
		}
		_, err := t.engine.publish(ctx, tx, p)
		return err
	})
	if err != nil {
		return nil, err
	}
	t.engine.signalTopic(req.GetTopic(), topic)
	return &topicv1.SendResponse{MessageId: id}, nil
}

func (t Topics) ListDeadLetters(ctx context.Context, req *topicv1.ListDeadLettersRequest) (*topicv1.ListDeadLettersResponse, error) {
	limit := int(req.GetLimit())
	if limit <= 0 {
		limit = defaultRunPage
	}
	args := []any{req.GetTopic(), req.GetConsumer(), limit + 1}
	query := `SELECT execution, message_id, published_at, payload, attempts, error, finished_at FROM ocel.runs
		WHERE topic = $1 AND consumer = $2 AND status IN ` + deadLetterStatuses
	if req.GetCursor() != "" {
		finished, execution, err := parseCursor(req.GetCursor())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		args = append(args, finished, execution)
		query += " AND (finished_at, execution) > ($4, $5)"
	}
	rows, err := t.engine.pool.Query(ctx, query+" ORDER BY finished_at, execution LIMIT $3", args...)
	if err != nil {
		return nil, fmt.Errorf("list dead letters: %w", err)
	}
	type row struct {
		letter   *topicv1.DeadLetter
		finished time.Time
	}
	letters, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var execution, messageID, reason string
		var published, finished time.Time
		var payload []byte
		var attempts int32
		if err := r.Scan(&execution, &messageID, &published, &payload, &attempts, &reason, &finished); err != nil {
			return row{}, err
		}
		return row{finished: finished, letter: &topicv1.DeadLetter{
			Execution: execution,
			Message:   &topicv1.Message{Id: messageID, PublishedAt: timestampOf(published)},
			Payload:   valueOf(payload),
			Attempts:  attempts,
			Error:     reason,
			FailedAt:  timestampOf(finished),
		}}, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list dead letters: %w", err)
	}
	resp := &topicv1.ListDeadLettersResponse{}
	if len(letters) > limit {
		letters = letters[:limit]
		last := letters[limit-1]
		resp.NextCursor = cursorOf(last.finished, last.letter.GetExecution())
	}
	for _, l := range letters {
		resp.DeadLetters = append(resp.DeadLetters, l.letter)
	}
	return resp, nil
}

func (t Topics) RedriveDeadLetters(ctx context.Context, req *topicv1.RedriveDeadLettersRequest) (*topicv1.RedriveDeadLettersResponse, error) {
	ref, err := t.consumerOf(req.GetTopic(), req.GetConsumer())
	if err != nil {
		return nil, err
	}
	var redriven int64
	err = t.engine.inTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT execution, key, lane FROM ocel.runs
			WHERE topic = $1 AND consumer = $2 AND status IN `+deadLetterStatuses+` AND (cardinality($3::text[]) = 0 OR execution = ANY($3))
			FOR UPDATE`, req.GetTopic(), req.GetConsumer(), orEmpty(req.GetExecutions()))
		if err != nil {
			return err
		}
		type letter struct{ execution, key, lane string }
		letters, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (letter, error) {
			var l letter
			return l, r.Scan(&l.execution, &l.key, &l.lane)
		})
		if err != nil {
			return err
		}
		now := time.Now()
		for _, l := range letters {
			msgID, err := sendToQueue(ctx, tx, ref, l.execution, l.key, l.lane, now)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE ocel.runs SET status = 'queued', attempts = 0, error = '', due_at = $2,
				started_at = NULL, finished_at = NULL, queue_message = $3, revision = `+newRevisionSQL+` WHERE execution = $1`,
				l.execution, now, msgID); err != nil {
				return err
			}
		}
		redriven = int64(len(letters))
		return nil
	})
	if err != nil {
		return nil, err
	}
	t.engine.signal(ref.queue)
	return &topicv1.RedriveDeadLettersResponse{Redriven: redriven}, nil
}

func (t Topics) PurgeDeadLetters(ctx context.Context, req *topicv1.PurgeDeadLettersRequest) (*topicv1.PurgeDeadLettersResponse, error) {
	tag, err := t.engine.pool.Exec(ctx, `DELETE FROM ocel.runs
		WHERE topic = $1 AND consumer = $2 AND status IN `+deadLetterStatuses+` AND (cardinality($3::text[]) = 0 OR execution = ANY($3))`,
		req.GetTopic(), req.GetConsumer(), orEmpty(req.GetExecutions()))
	if err != nil {
		return nil, fmt.Errorf("purge dead letters: %w", err)
	}
	return &topicv1.PurgeDeadLettersResponse{Purged: tag.RowsAffected()}, nil
}

func (t Topics) CountDeadLetters(ctx context.Context, req *topicv1.CountDeadLettersRequest) (*topicv1.CountDeadLettersResponse, error) {
	var count int64
	if err := t.engine.pool.QueryRow(ctx, `SELECT count(*) FROM ocel.runs WHERE topic = $1 AND consumer = $2 AND status IN `+deadLetterStatuses,
		req.GetTopic(), req.GetConsumer()).Scan(&count); err != nil {
		return nil, fmt.Errorf("count dead letters: %w", err)
	}
	return &topicv1.CountDeadLettersResponse{Count: count}, nil
}
