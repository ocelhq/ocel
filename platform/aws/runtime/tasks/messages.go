package tasks

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

type queueMessage struct {
	Execution         string `json:"execution,omitempty"`
	Delivery          string `json:"delivery,omitempty"`
	Message           string `json:"message,omitempty"`
	PublishedAtMicros int64  `json:"publishedAt,omitempty"`
	DueAtMicros       int64  `json:"dueAt,omitempty"`
	Key               string `json:"key,omitempty"`
	Lane              string `json:"lane,omitempty"`
	MaxAttempts       int    `json:"maxAttempts,omitempty"`
	Payload           string `json:"payload,omitempty"`
}

func bodyOf(msg runs.Message) (string, error) {
	body, err := json.Marshal(queueMessage{
		Execution:         msg.Execution,
		Delivery:          msg.Delivery,
		Message:           msg.ID,
		PublishedAtMicros: microsOf(msg.PublishedAt),
		DueAtMicros:       microsOf(msg.DueAt),
		Key:               msg.Key,
		Lane:              string(msg.Lane),
		MaxAttempts:       msg.MaxAttempts,
		Payload:           string(msg.Payload),
	})
	return string(body), err
}

func messageOf(topic, consumer, body string) (runs.Message, error) {
	var decoded queueMessage
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return runs.Message{}, fmt.Errorf("the message is not one this engine sent: %w", err)
	}
	msg := runs.Message{
		Topic:       topic,
		Consumer:    consumer,
		Execution:   decoded.Execution,
		Delivery:    decoded.Delivery,
		ID:          decoded.Message,
		PublishedAt: timeOfMicros(decoded.PublishedAtMicros),
		DueAt:       timeOfMicros(decoded.DueAtMicros),
		Key:         decoded.Key,
		Lane:        provider.Lane(decoded.Lane),
		MaxAttempts: decoded.MaxAttempts,
	}
	if decoded.Payload != "" {
		msg.Payload = json.RawMessage(decoded.Payload)
	}
	return msg, nil
}

func executionOf(msg runs.Message) string {
	if msg.Execution != "" {
		return msg.Execution
	}
	return runs.ExecutionOf(msg.ID, msg.Consumer)
}

func groupOf(msg runs.Message, fallback string) string {
	if msg.Key != "" {
		return msg.Key
	}
	return fallback
}

func microsOf(at time.Time) int64 {
	if at.IsZero() {
		return 0
	}
	return at.UnixMicro()
}

func timeOfMicros(micros int64) time.Time {
	if micros == 0 {
		return time.Time{}
	}
	return time.UnixMicro(micros)
}
