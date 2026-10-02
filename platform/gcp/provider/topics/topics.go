package topics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/envelope"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

type Topics struct {
	deployment Deployment
}

type recordedMessage struct {
	Message string `json:"message"`
}

func (t Topics) declared(name string) (*contractv1.ManifestTopic, error) {
	topic, found := t.deployment.Declared[name]
	if !found || isTask(topic) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no topic named %q is deployed", name))
	}
	return topic, nil
}

func (t Topics) Send(ctx context.Context, req *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	topic, err := t.declared(req.GetTopic())
	if err != nil {
		return nil, err
	}
	if err := runs.RefuseNonJSON(req.GetPayload()); err != nil {
		return nil, err
	}
	now := time.Now()
	toPublish := publication{
		topicName:   req.GetTopic(),
		topic:       topic,
		messageID:   envelope.NewMessageID(now),
		publishedAt: now,
		dueAt:       now,
		payload:     req.GetPayload(),
		key:         req.GetKey(),
		lane:        string(runs.LaneOf(req.GetLane())),
	}
	if req.GetDueAt() != nil {
		toPublish.dueAt = req.GetDueAt().AsTime()
	}
	var idempotency provider.ExpiringRecord
	if key := req.GetIdempotencyKey(); key != "" {
		value, err := json.Marshal(recordedMessage{Message: toPublish.messageID})
		if err != nil {
			return nil, err
		}
		idempotency = provider.ExpiringRecord{
			Purpose: provider.RecordIdempotency, Topic: req.GetTopic(), Key: key, Value: value, ExpiresAt: now.Add(provider.DefaultIdempotencyKeyLife),
		}
		existing, created, err := t.deployment.Store().EnsureRecord(ctx, idempotency)
		if err != nil {
			return nil, err
		}
		if !created {
			var recorded recordedMessage
			if err := json.Unmarshal(existing.Value, &recorded); err != nil {
				return nil, fmt.Errorf("read the message idempotency key %q names: %w", key, err)
			}
			return &topicv1.SendResponse{MessageId: recorded.Message}, nil
		}
	}
	if err := t.deployment.publish(ctx, toPublish, t.deployment.delayTaskOf(toPublish)); err != nil {
		return nil, errors.Join(err, t.deployment.Store().deleteRecordsHolding(ctx, idempotency))
	}
	return &topicv1.SendResponse{MessageId: toPublish.messageID}, nil
}
