package topic

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"ocel.dev/internal/codec"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
	"ocel.dev/internal/proto/app/topic/v1/topicv1connect"
	"ocel.dev/runs"
)

// DeadLetters are the messages one consumer of a topic gave up on, kept until
// they are redriven or purged.
type DeadLetters struct {
	topic    string
	consumer string
	connect  func(access string) (topicv1connect.TopicServiceClient, error)
}

// A DeadLetter is a message a consumer gave up on.
type DeadLetter struct {
	// Execution is the consumer's handling of the message, which Redrive and
	// Purge name it by.
	Execution string
	// Message is the message as it was published.
	Message runs.Message
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
	cursor string
	limit  int32
}

type deadLetterListOption func(*deadLetterListSettings)

func (o deadLetterListOption) applyDeadLetterList(s *deadLetterListSettings) { o(s) }

// Limit is how many dead letters a page holds at most.
func Limit(deadLetters int) DeadLetterListOption {
	return deadLetterListOption(func(s *deadLetterListSettings) { s.limit = int32(deadLetters) })
}

// Cursor reads the page a previous page's NextCursor points at.
func Cursor(cursor string) DeadLetterListOption {
	return deadLetterListOption(func(s *deadLetterListSettings) { s.cursor = cursor })
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
	res, err := topic.ListDeadLetters(ctx, &topicv1.ListDeadLettersRequest{
		Topic:    d.topic,
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
	res, err := topic.RedriveDeadLetters(ctx, &topicv1.RedriveDeadLettersRequest{
		Topic:      d.topic,
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
	res, err := topic.PurgeDeadLetters(ctx, &topicv1.PurgeDeadLettersRequest{
		Topic:      d.topic,
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
	res, err := topic.CountDeadLetters(ctx, &topicv1.CountDeadLettersRequest{
		Topic:    d.topic,
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
	return &DeadLetter{
		Execution: wire.GetExecution(),
		Message:   runs.Message{ID: wire.GetMessage().GetId(), PublishedAt: codec.DecodeTimestamp(wire.GetMessage().GetPublishedAt())},
		Payload:   codec.ReadJSON(wire.GetPayload()),
		Attempts:  int(wire.GetAttempts()),
		Error:     wire.GetError(),
		FailedAt:  codec.DecodeTimestamp(wire.GetFailedAt()),
	}, nil
}
