package runs

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

type Message struct {
	Topic       string
	Consumer    string
	Execution   string
	Delivery    string
	ID          string
	PublishedAt time.Time
	DueAt       time.Time
	Key         string
	Lane        provider.Lane
	MaxAttempts int
	Payload     json.RawMessage
}

func (m Message) executionFor(consumer string) string {
	if m.Execution != "" {
		return m.Execution
	}
	return ExecutionOf(m.ID, consumer)
}

func (m Message) group() string {
	if m.Key != "" {
		return m.Key
	}
	return m.executionFor(m.Consumer)
}

type Queue interface {
	Send(ctx context.Context, msg Message) error
	Release(ctx context.Context, topic, consumer, handle string) error
}

type Received struct {
	Handle   string
	Message  Message
	Receives int
}

type Disposition struct {
	Done  bool
	After time.Duration
}

var done = Disposition{Done: true}

func redeliverAfter(wait time.Duration) Disposition {
	return Disposition{After: max(wait, 0)}
}

type ReceiveRequest struct {
	Topic    string
	Consumer string
	Max      int
	Wait     time.Duration
	Hidden   time.Duration
}

type Receiver interface {
	Receive(ctx context.Context, req ReceiveRequest) ([]Received, error)
	Extend(ctx context.Context, topic, consumer string, handles []string, hidden time.Duration) error
	Ack(ctx context.Context, topic, consumer string, handles []string) error
	Hide(ctx context.Context, topic, consumer, handle string, hidden time.Duration) error
}
