package pgmq

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	groupHeader     = "x-pgmq-group"
	pollInterval    = 200 * time.Millisecond
	maxPollInterval = 2 * time.Second
)

type queueMessage struct {
	Execution   string    `json:"execution,omitempty"`
	Delivery    string    `json:"delivery,omitempty"`
	ID          string    `json:"id"`
	PublishedAt time.Time `json:"publishedAt"`
	DueAt       time.Time `json:"dueAt"`
	Key         string    `json:"key,omitempty"`
	Lane        string    `json:"lane"`
	MaxAttempts int       `json:"maxAttempts,omitempty"`
	Payload     string    `json:"payload,omitempty"`
}

type queue struct {
	engine *Engine
}

func (q queue) Send(ctx context.Context, msg runs.Message) error {
	topic, found := q.engine.current().Topics[msg.Topic]
	if !found {
		return fmt.Errorf("no topic named %q is deployed", msg.Topic)
	}
	var sent []string
	err := q.engine.inTx(ctx, func(tx pgx.Tx) error {
		for _, consumer := range topic.GetConsumers() {
			if msg.Consumer != "" && msg.Consumer != consumer.GetName() {
				continue
			}
			name := queueName(msg.Topic, consumer.GetName())
			if err := sendTo(ctx, tx, name, topic, consumer, msg); err != nil {
				return err
			}
			sent = append(sent, name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, name := range sent {
		q.engine.signal(name)
	}
	return nil
}

func sendTo(ctx context.Context, tx pgx.Tx, name string, topic *contractv1.ManifestTopic, consumer *contractv1.ManifestConsumer, msg runs.Message) error {
	body, err := json.Marshal(queueMessage{
		Execution:   msg.Execution,
		Delivery:    msg.Delivery,
		ID:          msg.ID,
		PublishedAt: msg.PublishedAt,
		DueAt:       msg.DueAt,
		Key:         msg.Key,
		Lane:        string(laneOf(consumer, msg.Lane)),
		MaxAttempts: msg.MaxAttempts,
		Payload:     string(msg.Payload),
	})
	if err != nil {
		return err
	}
	var headers any
	if topic.GetOrdered() {
		group := msg.Key
		if group == "" {
			group = msg.Execution
		}
		if group == "" {
			group = runs.ExecutionOf(msg.ID, consumer.GetName())
		}
		encoded, err := json.Marshal(map[string]string{groupHeader: group})
		if err != nil {
			return err
		}
		headers = string(encoded)
	}
	if _, err := tx.Exec(ctx, "SELECT pgmq.send($1, $2::jsonb, $3::jsonb, $4::timestamptz)", name, string(body), headers, msg.DueAt); err != nil {
		return fmt.Errorf("send message %s to queue %s: %w", msg.ID, name, err)
	}
	return nil
}

func (q queue) Release(ctx context.Context, topic, consumer, handle string) error {
	id, err := strconv.ParseInt(handle, 10, 64)
	if err != nil {
		return fmt.Errorf("handle %q names no message of queue %s", handle, queueName(topic, consumer))
	}
	name := queueName(topic, consumer)
	if _, err := q.engine.pool.Exec(ctx, "SELECT pgmq.set_vt($1, $2::bigint, clock_timestamp())", name, id); err != nil {
		return fmt.Errorf("release message %d of queue %s: %w", id, name, err)
	}
	q.engine.signal(name)
	return nil
}

func secondsOf(d time.Duration) int {
	return max(int(math.Ceil(d.Seconds())), 1)
}

func (q queue) Receive(ctx context.Context, req runs.ReceiveRequest) ([]runs.Received, error) {
	name := queueName(req.Topic, req.Consumer)
	wake := q.engine.ensureWakeChannel(name)
	deadline := time.Now().Add(req.Wait)
	idle := pollInterval
	for {
		received, err := q.read(ctx, name, req)
		if err != nil || len(received) > 0 {
			return received, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil
		}
		timer := time.NewTimer(min(idle, remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-wake:
			idle = pollInterval
		case <-timer.C:
			idle = lengthenPollInterval(idle)
		}
		timer.Stop()
	}
}

func lengthenPollInterval(interval time.Duration) time.Duration {
	return min(interval*2, maxPollInterval)
}

func (q queue) read(ctx context.Context, name string, req runs.ReceiveRequest) ([]runs.Received, error) {
	topic, consumer, found := q.engine.current().consumer(req.Topic, req.Consumer)
	if !found {
		return nil, nil
	}
	hidden := secondsOf(req.Hidden)
	switch {
	case topic.GetOrdered() && consumer.GetBatch().GetSize() > 0:
		return q.readWith(ctx, "SELECT msg_id, read_ct, message FROM pgmq.read_grouped($1, $2, $3)", name, hidden, req.Max)
	case topic.GetOrdered():
		return q.readWith(ctx, "SELECT msg_id, read_ct, message FROM pgmq.read_grouped_head($1, $2, $3)", name, hidden, req.Max)
	}
	var read []runs.Received
	for lane, share := range q.engine.laneTurnsOf(name).share(lanesOf(consumer), req.Max) {
		condition, err := laneCondition(lane)
		if err != nil {
			return nil, err
		}
		got, err := q.readWith(ctx, "SELECT msg_id, read_ct, message FROM pgmq.read($1, $2, $3, $4::jsonb)", name, hidden, share, condition)
		if err != nil {
			return read, err
		}
		read = append(read, got...)
	}
	if rest := req.Max - len(read); rest > 0 {
		got, err := q.readWith(ctx, "SELECT msg_id, read_ct, message FROM pgmq.read($1, $2, $3)", name, hidden, rest)
		if err != nil {
			return read, err
		}
		read = append(read, got...)
	}
	return read, nil
}

func (q queue) readWith(ctx context.Context, query string, args ...any) ([]runs.Received, error) {
	rows, err := q.engine.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (runs.Received, error) {
		var id int64
		var reads int
		var body []byte
		if err := row.Scan(&id, &reads, &body); err != nil {
			return runs.Received{}, err
		}
		var msg queueMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			return runs.Received{}, fmt.Errorf("message %d is not one the engine sent: %w", id, err)
		}
		return runs.Received{Handle: strconv.FormatInt(id, 10), Receives: reads, Message: runs.Message{
			Execution:   msg.Execution,
			Delivery:    msg.Delivery,
			ID:          msg.ID,
			PublishedAt: msg.PublishedAt,
			DueAt:       msg.DueAt,
			Key:         msg.Key,
			Lane:        provider.Lane(msg.Lane),
			MaxAttempts: msg.MaxAttempts,
			Payload:     payloadOf(msg.Payload),
		}}, nil
	})
}

func payloadOf(text string) json.RawMessage {
	if text == "" {
		return nil
	}
	return json.RawMessage(text)
}

func idsOf(handles []string) ([]int64, error) {
	ids := make([]int64, 0, len(handles))
	for _, handle := range handles {
		id, err := strconv.ParseInt(handle, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("handle %q names no queue message", handle)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (q queue) Extend(ctx context.Context, topic, consumer string, handles []string, hidden time.Duration) error {
	ids, err := idsOf(handles)
	if err != nil {
		return err
	}
	_, err = q.engine.pool.Exec(ctx, "SELECT pgmq.set_vt($1, $2::bigint[], clock_timestamp() + make_interval(secs => $3))", queueName(topic, consumer), ids, hidden.Seconds())
	return err
}

func (q queue) Ack(ctx context.Context, topic, consumer string, handles []string) error {
	ids, err := idsOf(handles)
	if err != nil {
		return err
	}
	_, err = q.engine.pool.Exec(ctx, "SELECT pgmq.delete($1, $2::bigint[])", queueName(topic, consumer), ids)
	return err
}

func (q queue) Hide(ctx context.Context, topic, consumer, handle string, hidden time.Duration) error {
	ids, err := idsOf([]string{handle})
	if err != nil {
		return err
	}
	name := queueName(topic, consumer)
	if _, err := q.engine.pool.Exec(ctx, "SELECT pgmq.set_vt($1, $2::bigint, clock_timestamp() + make_interval(secs => $3))", name, ids[0], max(hidden, 0).Seconds()); err != nil {
		return err
	}
	if hidden <= 0 {
		q.engine.signal(name)
	}
	return nil
}

func laneOf(consumer *contractv1.ManifestConsumer, asked provider.Lane) provider.Lane {
	if asked == "" {
		return provider.LaneDefault
	}
	if reads := consumer.GetLanes(); len(reads) > 0 {
		for _, read := range reads {
			if runs.LaneOf(read) == asked {
				return asked
			}
		}
		return provider.LaneDefault
	}
	return asked
}
