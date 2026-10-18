package topics

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	MessageAttribute     = "ocel-message"
	PublishedAtAttribute = "ocel-published-at"
	DueAtAttribute       = "ocel-due-at"
	MaxAttemptsAttribute = "ocel-max-attempts"
	LaneAttribute        = "ocel-lane"
	ScheduleAttribute    = "ocel-schedule"
	ConsumerAttribute    = "ocel-consumer"
	ExecutionAttribute   = "ocel-execution"
	DeliveryAttribute    = "ocel-delivery"
)

func pubsubMessageOf(msg runs.Message, ordered bool) *pubsub.PubsubMessage {
	attributes := map[string]string{
		MessageAttribute:     msg.ID,
		PublishedAtAttribute: msg.PublishedAt.UTC().Format(time.RFC3339Nano),
		DueAtAttribute:       msg.DueAt.UTC().Format(time.RFC3339Nano),
	}
	optional := map[string]string{
		LaneAttribute:      string(msg.Lane),
		ConsumerAttribute:  msg.Consumer,
		ExecutionAttribute: msg.Execution,
		DeliveryAttribute:  msg.Delivery,
	}
	if msg.MaxAttempts > 0 {
		optional[MaxAttemptsAttribute] = strconv.Itoa(msg.MaxAttempts)
	}
	for name, value := range optional {
		if value != "" {
			attributes[name] = value
		}
	}
	message := &pubsub.PubsubMessage{Data: base64.StdEncoding.EncodeToString(msg.Payload), Attributes: attributes}
	if ordered {
		message.OrderingKey = msg.Key
	}
	return message
}

func messageOf(received *pubsub.PubsubMessage, now time.Time) (runs.Message, error) {
	payload, err := base64.StdEncoding.DecodeString(received.Data)
	if err != nil {
		return runs.Message{}, fmt.Errorf("the data of Pub/Sub message %s is not base64: %w", received.MessageId, err)
	}
	attributes := received.Attributes
	publishTime, err := time.Parse(time.RFC3339Nano, received.PublishTime)
	idTime := publishTime
	if err != nil {
		idTime, publishTime = time.Unix(0, 0), now
	}
	msg := runs.Message{
		Consumer:    attributes[ConsumerAttribute],
		Execution:   attributes[ExecutionAttribute],
		Delivery:    attributes[DeliveryAttribute],
		ID:          attributes[MessageAttribute],
		PublishedAt: publishTime,
		Key:         received.OrderingKey,
		Lane:        provider.Lane(attributes[LaneAttribute]),
		Payload:     payload,
	}
	if msg.ID == "" {
		msg.ID = runs.MessageIDFrom(idTime, received.MessageId)
	}
	if at, err := time.Parse(time.RFC3339Nano, attributes[PublishedAtAttribute]); err == nil {
		msg.PublishedAt = at
	}
	msg.DueAt = msg.PublishedAt
	if at, err := time.Parse(time.RFC3339Nano, attributes[DueAtAttribute]); err == nil {
		msg.DueAt = at
	}
	msg.MaxAttempts, _ = strconv.Atoi(attributes[MaxAttemptsAttribute])
	if _, scheduled := attributes[ScheduleAttribute]; scheduled {
		msg.Payload = runs.CronPayload(publishTime)
	}
	return msg, nil
}
