package task

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"ocel.dev/internal/delivery"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	"ocel.dev/worker"
)

// An Option tunes the task [New] or [NewBatch] declares.
type Option interface {
	applyTask(*settings)
}

type settings struct {
	config *resourcesv1.TaskConfig
	hooks  hookFuncs
}

type option func(*settings)

func (o option) applyTask(s *settings) { o(s) }

// A Retry is how often, and how far apart, a failed attempt of the task is
// tried again, given as an [Option]. A field left zero is left to the
// default: 3 attempts, 1s after the first failure, backing off to at most 60s.
type Retry struct {
	// MaxAttempts is how many attempts a run gets in all, 1 to 100.
	MaxAttempts int
	// MinDelay is how long the first retry waits.
	MinDelay time.Duration
	// MaxDelay is the longest any retry waits, at most 600s.
	MaxDelay time.Duration
}

func (r Retry) applyTask(s *settings) {
	s.config.Retry = delivery.EncodeRetry(r.MaxAttempts, r.MinDelay, r.MaxDelay)
}

// Schema is the JSON Schema document a payload is checked against, written
// out as JSON. Ocel derives none from a Go type, so a task without one takes
// any payload its type decodes from.
func Schema(jsonSchema string) Option {
	return option(func(s *settings) { s.config.Schema = jsonSchema })
}

// Ordered delivers the runs that share a [Key] one at a time, in the order
// they were triggered, and holds the key back while one of them fails.
func Ordered() Option {
	return option(func(s *settings) { s.config.Ordered = true })
}

// Concurrency is how many runs of the task are handled at once, at most, 1 to
// 1000.
func Concurrency(limit int) Option {
	return option(func(s *settings) { s.config.Concurrency = int32(limit) })
}

// MaxDuration is how long one attempt may run. An attempt that runs longer
// ends the run as timed out.
func MaxDuration(limit time.Duration) Option {
	return option(func(s *settings) { s.config.MaxDuration = durationpb.New(limit) })
}

// BatchTimeout is how long a task [NewBatch] declares waits to fill a batch
// before taking what has arrived, up to 300s.
func BatchTimeout(wait time.Duration) Option {
	return option(func(s *settings) { delivery.EnsureBatchPolicy(&s.config.Batch).Timeout = durationpb.New(wait) })
}

// UseWorker runs the task on w rather than on the default worker, named
// "worker".
func UseWorker(w *worker.Worker) Option {
	return option(func(s *settings) { s.config.Worker = w.Name() })
}

// A TTL is how long a run may wait to start before it expires unrun. Given to
// [New] it applies to every run; given to Trigger, to that run alone.
type TTL time.Duration

func (t TTL) applyTask(s *settings) { s.config.Ttl = durationpb.New(time.Duration(t)) }

func (t TTL) applyTrigger(s *triggerSettings) { s.ttl = durationpb.New(time.Duration(t)) }

// Cron triggers the task on a schedule, given as a five-field cron
// expression.
func Cron(expression string) Option {
	return option(func(s *settings) { s.config.Cron = expression })
}

// Middleware wraps every attempt of the task. It calls next to go on with the
// attempt, and returns its error or one of its own.
func Middleware(fn func(ctx context.Context, next func(context.Context) error) error) Option {
	return option(func(s *settings) { s.hooks.middleware = fn })
}
