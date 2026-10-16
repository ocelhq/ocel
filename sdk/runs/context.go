package runs

import (
	"context"
	"errors"
	"time"
)

// ErrAbort fails a run or message without retrying it. Return it, or an error
// wrapping it, from a task's run or a consumer's handler:
//
//	return fmt.Errorf("image %s is not a png: %w", key, runs.ErrAbort)
var ErrAbort = errors.New("aborted without retrying")

// A Kind is what a [Context] is running.
type Kind string

const (
	// KindTask is a run of a task.
	KindTask Kind = "task"
	// KindConsumer is a delivery to a topic's consumer.
	KindConsumer Kind = "consumer"
)

// A Context is what a running task or consumer, and the hooks and middleware
// around it, know about the attempt. [FromContext] reads it from the context
// they are handed; that context is cancelled when the attempt is.
type Context struct {
	// Kind is whether a task or a consumer is running.
	Kind Kind
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

// FromContext reads the [Context] from the context a task's run, a consumer's
// handler, or a hook or middleware around them is handed. It reports false
// for any other context.
func FromContext(ctx context.Context) (Context, bool) {
	run, ok := ctx.Value(contextKey{}).(Context)
	return run, ok
}

// NewContext returns a copy of ctx carrying run, which [FromContext] reads. It
// is useful for testing a task's run or a consumer's handler.
func NewContext(ctx context.Context, run Context) context.Context {
	return context.WithValue(ctx, contextKey{}, run)
}

type contextKey struct{}
