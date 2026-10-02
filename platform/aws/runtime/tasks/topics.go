package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/envelope"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

var _ topicv1connect.TopicServiceHandler = Topics{}

type Topics struct {
	engine *Engine
}

func (e *Engine) Topics() Topics { return Topics{engine: e} }

type recordedMessage struct {
	Message string `json:"message"`
}

func (t Topics) Send(ctx context.Context, req *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	topic, found := t.engine.topic(req.GetTopic())
	if !found || runs.IsTask(topic.Declared) || topic.SNS == "" {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no topic named %q is deployed", req.GetTopic()))
	}
	if err := runs.RefuseNonJSON(req.GetPayload()); err != nil {
		return nil, err
	}
	now := time.Now()
	messageID := envelope.NewMessageID(now)
	if key := req.GetIdempotencyKey(); key != "" {
		encoded, err := json.Marshal(recordedMessage{Message: messageID})
		if err != nil {
			return nil, err
		}
		held, created, err := t.engine.store.EnsureRecord(ctx, provider.ExpiringRecord{Purpose: provider.RecordIdempotency, Topic: req.GetTopic(), Key: key, Value: encoded, ExpiresAt: now.Add(provider.DefaultIdempotencyKeyLife)})
		if err != nil {
			return nil, err
		}
		if !created {
			var recorded recordedMessage
			_ = json.Unmarshal(held.Value, &recorded)
			return &topicv1.SendResponse{MessageId: recorded.Message}, nil
		}
	}
	msg := queueMessage{
		Message:           messageID,
		PublishedAtMicros: now.UnixMicro(),
		DueAtMicros:       now.UnixMicro(),
		Key:               req.GetKey(),
		Lane:              string(runs.LaneOf(req.GetLane())),
		Payload:           req.GetPayload(),
	}
	if req.GetLane() == topicv1.Lane_LANE_UNSPECIFIED {
		msg.Lane = ""
	}
	if req.GetDueAt() != nil {
		msg.DueAtMicros = req.GetDueAt().AsTime().UnixMicro()
	}
	if err := t.engine.publish(ctx, req.GetTopic(), topic.SNS, topic.Declared.Ordered, msg); err != nil {
		if key := req.GetIdempotencyKey(); key != "" {
			_ = t.engine.store.deleteRecord(ctx, provider.RecordIdempotency, req.GetTopic(), key)
		}
		return nil, err
	}
	return &topicv1.SendResponse{MessageId: messageID}, nil
}

func (t Topics) deployedConsumer(topicName, consumer string) (deployedConsumer, error) {
	deployed, found := t.engine.consumer(topicName, consumer)
	if !found || deployed.isTask() {
		return deployedConsumer{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("topic %q has no consumer %q deployed", topicName, consumer))
	}
	return deployed, nil
}

func deadLetterFilter(consumer string) runFilter {
	return filterOf(consumer, []provider.RunStatus{provider.RunFailed}, nil)
}

func (t Topics) ListDeadLetters(ctx context.Context, req *topicv1.ListDeadLettersRequest) (*topicv1.ListDeadLettersResponse, error) {
	limit := runs.PageLimit(int(req.GetLimit()))
	before := ""
	if req.GetCursor() != "" {
		parsed, err := parseCursor(req.GetCursor())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		before = parsed
	}
	items, err := t.engine.store.listMatching(ctx, req.GetTopic(), before, deadLetterFilter(req.GetConsumer()), limit+1)
	if err != nil {
		return nil, err
	}
	resp := &topicv1.ListDeadLettersResponse{}
	if len(items) > limit {
		items = items[:limit]
		resp.NextCursor = cursorOf(items[limit-1].SK)
	}
	for _, item := range items {
		resp.DeadLetters = append(resp.DeadLetters, &topicv1.DeadLetter{
			Execution: item.SK,
			Message:   &topicv1.Message{Id: item.Message, PublishedAt: runs.TimestampOf(timeOfMicros(item.PublishedAtMicros))},
			Payload:   json.RawMessage(item.Payload),
			Attempts:  int32(item.Attempts),
			Error:     item.Error,
			FailedAt:  runs.TimestampOf(timeOfMicros(item.FinishedAtMicros)),
		})
	}
	return resp, nil
}

func (t Topics) deadLetters(ctx context.Context, topic, consumer string, executions []string) ([]runItem, error) {
	if len(executions) == 0 {
		return t.engine.store.listMatching(ctx, topic, "", deadLetterFilter(consumer), maxRunPage)
	}
	var letters []runItem
	for _, execution := range executions {
		item, found, err := t.engine.store.readRun(ctx, topic, execution, true)
		if err != nil {
			return nil, err
		}
		if found && item.Consumer == consumer && provider.RunStatus(item.Status) == provider.RunFailed {
			letters = append(letters, item)
		}
	}
	return letters, nil
}

func (t Topics) RedriveDeadLetters(ctx context.Context, req *topicv1.RedriveDeadLettersRequest) (*topicv1.RedriveDeadLettersResponse, error) {
	deployed, err := t.deployedConsumer(req.GetTopic(), req.GetConsumer())
	if err != nil {
		return nil, err
	}
	letters, err := t.deadLetters(ctx, req.GetTopic(), req.GetConsumer(), req.GetExecutions())
	if err != nil {
		return nil, err
	}
	var redriven int64
	for _, letter := range letters {
		now := time.Now()
		token := envelope.NewMessageID(now)
		_, err := t.engine.store.updateRun(ctx, letter.Topic, letter.SK, change{
			set:        map[string]any{"status": string(provider.RunQueued), "attempts": 0, "error": "", "due_at": now.UnixMicro(), "delivery": token, "expires_at": expiresAtUnixAfter(now.UnixMicro())},
			remove:     []string{"started_at", "finished_at"},
			condition:  "#status = :failed AND #delivery = :token",
			conditions: map[string]any{":failed": string(provider.RunFailed), ":token": letter.Delivery},
		})
		if err != nil {
			continue
		}
		msg := queueMessage{Execution: letter.SK, Delivery: token, Message: letter.Message, PublishedAtMicros: letter.PublishedAtMicros, Key: letter.Key, Lane: letter.Lane, Payload: json.RawMessage(letter.Payload)}
		if err := t.engine.enqueue(ctx, deployed, msg, 0); err != nil {
			return nil, err
		}
		redriven++
	}
	return &topicv1.RedriveDeadLettersResponse{Redriven: redriven}, nil
}

func (t Topics) PurgeDeadLetters(ctx context.Context, req *topicv1.PurgeDeadLettersRequest) (*topicv1.PurgeDeadLettersResponse, error) {
	letters, err := t.deadLetters(ctx, req.GetTopic(), req.GetConsumer(), req.GetExecutions())
	if err != nil {
		return nil, err
	}
	var purged int64
	for _, letter := range letters {
		deleted, err := t.engine.store.deleteRun(ctx, letter.Topic, letter.SK, "#status = :failed AND #consumer = :consumer",
			map[string]any{":failed": string(provider.RunFailed), ":consumer": req.GetConsumer()})
		if err != nil {
			return nil, err
		}
		if deleted {
			purged++
		}
	}
	return &topicv1.PurgeDeadLettersResponse{Purged: purged}, nil
}

func (t Topics) CountDeadLetters(ctx context.Context, req *topicv1.CountDeadLettersRequest) (*topicv1.CountDeadLettersResponse, error) {
	var count int64
	before := ""
	for {
		items, err := t.engine.store.listMatching(ctx, req.GetTopic(), before, deadLetterFilter(req.GetConsumer()), maxRunPage)
		if err != nil {
			return nil, err
		}
		count += int64(len(items))
		if len(items) < maxRunPage {
			return &topicv1.CountDeadLettersResponse{Count: count}, nil
		}
		before = items[len(items)-1].SK
	}
}
