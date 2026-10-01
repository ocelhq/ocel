package topics

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/envelope"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Topics struct {
	deployment Deployment
}

type recordedMessage struct {
	Message string `json:"message"`
}

func (t Topics) topic(name string) (*contractv1.ManifestTopic, error) {
	topic, found := t.deployment.Declared[name]
	if !found || isTask(topic) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no topic named %q is deployed", name))
	}
	return topic, nil
}

func (t Topics) Send(ctx context.Context, req *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	topic, err := t.topic(req.GetTopic())
	if err != nil {
		return nil, err
	}
	if err := refuseNonJSON(req.GetPayload()); err != nil {
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
		lane:        laneName(req.GetLane()),
	}
	if key := req.GetIdempotencyKey(); key != "" {
		value, err := json.Marshal(recordedMessage{Message: toPublish.messageID})
		if err != nil {
			return nil, err
		}
		existing, created, err := t.deployment.Store().EnsureRecord(ctx, provider.ExpiringRecord{
			Purpose: provider.RecordIdempotency, Topic: req.GetTopic(), Key: key, Value: value, ExpiresAt: now.Add(provider.DefaultIdempotencyKeyLife),
		})
		if err != nil {
			return nil, err
		}
		if !created {
			var recorded recordedMessage
			_ = json.Unmarshal(existing.Value, &recorded)
			return &topicv1.SendResponse{MessageId: recorded.Message}, nil
		}
	}
	if err := t.deployment.publishNow(ctx, toPublish); err != nil {
		return nil, err
	}
	return &topicv1.SendResponse{MessageId: toPublish.messageID}, nil
}
