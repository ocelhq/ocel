package ocel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	topicv1 "ocel.dev/internal/proto/app/topic/v1"
	"ocel.dev/internal/proto/app/topic/v1/topicv1connect"
)

// DeadLetters are the messages one consumer of a topic gave up on, kept until
// they are redriven or purged.
type DeadLetters struct {
	consumer string
	connect  func(access string) (*boundResource[topicv1connect.TopicServiceClient], error)
}

// A DeadLetter is a message a consumer gave up on.
type DeadLetter struct {
	// Execution is the consumer's handling of the message, which Redrive and
	// Purge name it by.
	Execution string
	// Message is the message as it was published.
	Message Message
	// Payload is the message's payload as JSON.
	Payload json.RawMessage
	// Attempts is how many attempts the consumer made.
	Attempts int
	// Error is what the last attempt failed with.
	Error string
	// FailedAt is when the last attempt failed.
	FailedAt time.Time
}

// A DeadLetterPage is one page of a dead-letter listing.
type DeadLetterPage struct {
	// DeadLetters are the dead letters on this page.
	DeadLetters []*DeadLetter
	// NextCursor reads the next page when passed to [Cursor], and is empty on
	// the last page.
	NextCursor string
}

// A DeadLetterListOption pages through a dead-letter listing.
type DeadLetterListOption interface {
	applyDeadLetterList(*deadLetterListSettings)
}

type deadLetterListSettings struct {
	pageSettings
}

// List reads one page of the consumer's dead letters.
func (d *DeadLetters) List(ctx context.Context, opts ...DeadLetterListOption) (*DeadLetterPage, error) {
	topic, err := d.connect(d.formatAccess("List"))
	if err != nil {
		return nil, err
	}
	settings := deadLetterListSettings{}
	for _, opt := range opts {
		opt.applyDeadLetterList(&settings)
	}
	res, err := topic.client.ListDeadLetters(ctx, &topicv1.ListDeadLettersRequest{
		Topic:    topic.name,
		Consumer: d.consumer,
		Cursor:   settings.cursor,
		Limit:    settings.limit,
	})
	if err != nil {
		return nil, err
	}
	page := &DeadLetterPage{NextCursor: res.GetNextCursor()}
	for _, wire := range res.GetDeadLetters() {
		letter, err := decodeDeadLetter(wire)
		if err != nil {
			return nil, err
		}
		page.DeadLetters = append(page.DeadLetters, letter)
	}
	return page, nil
}

// Redrive sends the dead letters named by executions back to the consumer,
// or every one of them when none is named, and returns how many it sent.
func (d *DeadLetters) Redrive(ctx context.Context, executions ...string) (int64, error) {
	topic, err := d.connect(d.formatAccess("Redrive"))
	if err != nil {
		return 0, err
	}
	res, err := topic.client.RedriveDeadLetters(ctx, &topicv1.RedriveDeadLettersRequest{
		Topic:      topic.name,
		Consumer:   d.consumer,
		Executions: executions,
	})
	if err != nil {
		return 0, err
	}
	return res.GetRedriven(), nil
}

// Purge deletes the dead letters named by executions, or every one of them
// when none is named, and returns how many it deleted.
func (d *DeadLetters) Purge(ctx context.Context, executions ...string) (int64, error) {
	topic, err := d.connect(d.formatAccess("Purge"))
	if err != nil {
		return 0, err
	}
	res, err := topic.client.PurgeDeadLetters(ctx, &topicv1.PurgeDeadLettersRequest{
		Topic:      topic.name,
		Consumer:   d.consumer,
		Executions: executions,
	})
	if err != nil {
		return 0, err
	}
	return res.GetPurged(), nil
}

// Count is how many dead letters the consumer has.
func (d *DeadLetters) Count(ctx context.Context) (int64, error) {
	topic, err := d.connect(d.formatAccess("Count"))
	if err != nil {
		return 0, err
	}
	res, err := topic.client.CountDeadLetters(ctx, &topicv1.CountDeadLettersRequest{
		Topic:    topic.name,
		Consumer: d.consumer,
	})
	if err != nil {
		return 0, err
	}
	return res.GetCount(), nil
}

func (d *DeadLetters) formatAccess(op string) string {
	return fmt.Sprintf("DeadLetter(%q).%s", d.consumer, op)
}

func decodeDeadLetter(wire *topicv1.DeadLetter) (*DeadLetter, error) {
	payload, err := encodeValueAsJSON(wire.GetPayload())
	if err != nil {
		return nil, err
	}
	return &DeadLetter{
		Execution: wire.GetExecution(),
		Message:   Message{ID: wire.GetMessage().GetId(), PublishedAt: decodeTimestamp(wire.GetMessage().GetPublishedAt())},
		Payload:   payload,
		Attempts:  int(wire.GetAttempts()),
		Error:     wire.GetError(),
		FailedAt:  decodeTimestamp(wire.GetFailedAt()),
	}, nil
}
