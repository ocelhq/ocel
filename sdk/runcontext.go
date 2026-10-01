package ocel

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
)

// A RunKind is what a [RunContext] is running.
type RunKind string

const (
	// KindTask is a run of a task.
	KindTask RunKind = "task"
	// KindConsumer is a delivery to a topic's consumer.
	KindConsumer RunKind = "consumer"
)

// A RunContext is what a running task or consumer, and the hooks and
// middleware around it, know about the attempt. [RunFrom] reads it from the
// context they are handed; that context is cancelled when the attempt is.
type RunContext struct {
	// Kind is whether a task or a consumer is running.
	Kind RunKind
	// Name is the task's or the consumer's name.
	Name string
	// Topic is the topic the message was sent to, which for a task is the
	// task's name.
	Topic string
	// ID is the execution this attempt belongs to: for a task, the run's id.
	// A batch carries the first message's.
	ID string
	// Attempt is which attempt this is. A delivery that does not say is
	// attempt 1 of 1.
	Attempt Attempt
	// Message is the message being handled, the first of a batch.
	Message Message
}

// An Attempt is one try at a run or a message.
type Attempt struct {
	// Number counts attempts from 1.
	Number int
	// Of is how many attempts there are in all.
	Of int
	// FirstAttemptedAt is when the first attempt started.
	FirstAttemptedAt time.Time
}

// A Message is a payload as it was published to a topic or a task.
type Message struct {
	// ID is the message's id.
	ID string
	// PublishedAt is when the message was published.
	PublishedAt time.Time
}

type runContextKey struct{}

// RunFrom reads the [RunContext] from the context a task's run, a consumer's
// handler, or a hook or middleware around them is handed. It reports false
// for any other context.
func RunFrom(ctx context.Context) (RunContext, bool) {
	run, ok := ctx.Value(runContextKey{}).(RunContext)
	return run, ok
}

func newRunContext(r *route, envelope *topicv1.Envelope) RunContext {
	id, message, attempt := envelope.GetExecution(), envelope.GetMessage(), envelope.GetAttempt()
	if batch := envelope.GetMessages(); len(batch) > 0 {
		first := batch[0]
		id = first.GetExecution()
		if first.GetMessage() != nil {
			message = first.GetMessage()
		}
		if first.GetAttempt() != nil {
			attempt = first.GetAttempt()
		}
	}
	run := RunContext{
		Kind:    r.kind,
		Name:    r.name,
		Topic:   envelope.GetTopic(),
		ID:      id,
		Attempt: Attempt{Number: 1, Of: 1},
		Message: Message{ID: message.GetId(), PublishedAt: decodeTimestamp(message.GetPublishedAt())},
	}
	if attempt != nil {
		run.Attempt = Attempt{
			Number:           int(attempt.GetNumber()),
			Of:               int(attempt.GetOf()),
			FirstAttemptedAt: decodeTimestamp(attempt.GetFirstAttemptedAt()),
		}
	}
	return run
}

func decodeTimestamp(timestamp *timestamppb.Timestamp) time.Time {
	if timestamp == nil {
		return time.Time{}
	}
	return timestamp.AsTime()
}
