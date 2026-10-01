package ocel

import (
	"context"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
)

// A TaskOption tunes the task [Task] or [BatchTask] declares.
type TaskOption interface {
	applyTask(*taskSettings)
}

// A TaskTopicOption tunes a task or a topic alike.
type TaskTopicOption interface {
	TaskOption
	TopicOption
}

// A TaskConsumerOption tunes a task or a consumer alike.
type TaskConsumerOption interface {
	TaskOption
	ConsumerOption
}

// A TaskTopicConsumerOption tunes a task, a topic or a consumer alike.
type TaskTopicConsumerOption interface {
	TaskOption
	TopicOption
	ConsumerOption
}

// A TaskConsumerWorkerOption tunes a task, a consumer or a worker alike.
type TaskConsumerWorkerOption interface {
	TaskOption
	ConsumerOption
	WorkerOption
}

// A TaskWorkerOption tunes a task or a worker alike.
type TaskWorkerOption interface {
	TaskOption
	WorkerOption
}

// A TaskTriggerOption tunes a task's declaration, or one trigger of it.
type TaskTriggerOption interface {
	TaskOption
	TriggerOption
}

// A RetryPolicy is how often, and how far apart, a failed attempt is tried
// again. A field left zero is left to the default: 3 attempts, 1s after the
// first failure, backing off to at most 60s.
type RetryPolicy struct {
	// MaxAttempts is how many attempts a run or message gets in all, 1 to 100.
	MaxAttempts int
	// MinDelay is how long the first retry waits.
	MinDelay time.Duration
	// MaxDelay is the longest any retry waits, at most 600s.
	MaxDelay time.Duration
}

func (p RetryPolicy) encode() *resourcesv1.RetryPolicy {
	return &resourcesv1.RetryPolicy{
		MaxAttempts: int32(p.MaxAttempts),
		MinDelay:    newOptionalDuration(p.MinDelay),
		MaxDelay:    newOptionalDuration(p.MaxDelay),
	}
}

type taskSettings struct {
	config *resourcesv1.TaskConfig
	hooks  hookFuncs
}

type option struct {
	task           func(*taskSettings)
	topic          func(*topicSettings)
	consumer       func(*consumerSettings)
	worker         func(*workerSettings)
	trigger        func(*triggerSettings)
	send           func(*sendSettings)
	runList        func(*runListSettings)
	deadLetterList func(*deadLetterListSettings)
}

func (o option) applyTask(s *taskSettings)                     { o.task(s) }
func (o option) applyTopic(s *topicSettings)                   { o.topic(s) }
func (o option) applyConsumer(s *consumerSettings)             { o.consumer(s) }
func (o option) applyWorker(s *workerSettings)                 { o.worker(s) }
func (o option) applyTrigger(s *triggerSettings)               { o.trigger(s) }
func (o option) applySend(s *sendSettings)                     { o.send(s) }
func (o option) applyRunList(s *runListSettings)               { o.runList(s) }
func (o option) applyDeadLetterList(s *deadLetterListSettings) { o.deadLetterList(s) }

// Schema is the JSON Schema document a payload is checked against, written
// out as JSON. Ocel derives none from a Go type, so a task or topic without
// one takes any payload P decodes from.
func Schema(jsonSchema string) TaskTopicOption {
	return option{
		task:  func(s *taskSettings) { s.config.Schema = jsonSchema },
		topic: func(s *topicSettings) { s.config.Schema = jsonSchema },
	}
}

// Ordered delivers the runs or messages that share a key one at a time, in
// the order they were sent, and holds the key back while one of them fails.
func Ordered() TaskTopicOption {
	return option{
		task:  func(s *taskSettings) { s.config.Ordered = true },
		topic: func(s *topicSettings) { s.config.Ordered = true },
	}
}

// Retry is the [RetryPolicy] a failed attempt is tried again under.
func Retry(policy RetryPolicy) TaskTopicConsumerOption {
	return option{
		task:     func(s *taskSettings) { s.config.Retry = policy.encode() },
		topic:    func(s *topicSettings) { s.config.Retry = policy.encode() },
		consumer: func(s *consumerSettings) { s.config.Retry = policy.encode() },
	}
}

// Concurrency is how many runs or messages are handled at once, at most: by
// one task or consumer, or by everything a worker serves, 1 to 1000.
func Concurrency(limit int) TaskConsumerWorkerOption {
	return option{
		task:     func(s *taskSettings) { s.config.Concurrency = int32(limit) },
		consumer: func(s *consumerSettings) { s.config.Concurrency = int32(limit) },
		worker:   func(s *workerSettings) { s.concurrency = limit },
	}
}

// MaxDuration is how long one attempt may run. A task attempt that runs
// longer ends the run as timed out; a consumer attempt that runs longer is a
// failed attempt, and is retried.
func MaxDuration(limit time.Duration) TaskConsumerOption {
	return option{
		task:     func(s *taskSettings) { s.config.MaxDuration = durationpb.New(limit) },
		consumer: func(s *consumerSettings) { s.config.MaxDuration = durationpb.New(limit) },
	}
}

// BatchTimeout is how long a [BatchTask] or a batch consumer waits to fill a
// batch before taking what has arrived, up to 300s.
func BatchTimeout(wait time.Duration) TaskConsumerOption {
	return option{
		task:     func(s *taskSettings) { ensureBatchPolicy(&s.config.Batch).Timeout = durationpb.New(wait) },
		consumer: func(s *consumerSettings) { ensureBatchPolicy(&s.config.Batch).Timeout = durationpb.New(wait) },
	}
}

// UseWorker runs a task or consumer on worker rather than on the default
// worker, named "worker".
func UseWorker(worker *WorkerDefinition) TaskConsumerOption {
	return option{
		task:     func(s *taskSettings) { s.config.Worker = worker.name },
		consumer: func(s *consumerSettings) { s.config.Worker = worker.name },
	}
}

// TTL is how long a run may wait to start before it expires unrun. Given to
// [Task] it applies to every run; given to Trigger, to that run alone.
func TTL(limit time.Duration) TaskTriggerOption {
	return option{
		task:    func(s *taskSettings) { s.config.Ttl = durationpb.New(limit) },
		trigger: func(s *triggerSettings) { s.ttl = durationpb.New(limit) },
	}
}

// Cron triggers the task on a schedule, given as a five-field cron
// expression.
func Cron(expression string) TaskOption {
	return option{task: func(s *taskSettings) { s.config.Cron = expression }}
}

// Middleware wraps every attempt of a task, or every run of everything a
// worker serves. It calls next to go on with the attempt, and returns its
// error or one of its own.
func Middleware(fn func(ctx context.Context, next func(context.Context) error) error) TaskWorkerOption {
	return option{
		task:   func(s *taskSettings) { s.hooks.middleware = fn },
		worker: func(s *workerSettings) { s.middleware = fn },
	}
}

// OnStartAttempt is called before every attempt of a task, inside the
// worker's middleware. An error it returns fails the attempt.
func OnStartAttempt[P any](fn func(ctx context.Context, payload P) error) TaskOption {
	return option{task: func(s *taskSettings) { s.hooks.onStartAttempt = fn }}
}

// OnSuccess is called once a run has succeeded, with what it returned. An
// error it returns is reported, and does not change the run's outcome.
func OnSuccess[P, R any](fn func(ctx context.Context, payload P, output R) error) TaskOption {
	return option{task: func(s *taskSettings) { s.hooks.onSuccess = fn }}
}

// OnFailure is called once a run has failed for good: aborted, or out of
// attempts. An error it returns is reported, and does not change the run's
// outcome.
func OnFailure[P any](fn func(ctx context.Context, payload P, err error) error) TaskOption {
	return option{task: func(s *taskSettings) { s.hooks.onFailure = fn }}
}

// A RunResult is how a run ended: with Output when Err is nil, or failed with
// Err.
type RunResult[R any] struct {
	// Output is what the run returned.
	Output R
	// Err is why the run failed, or nil when it succeeded.
	Err error
}

// OnComplete is called once a run has ended either way, after [OnSuccess] or
// [OnFailure]. An error it returns is reported, and does not change the run's
// outcome.
func OnComplete[P, R any](fn func(ctx context.Context, payload P, result RunResult[R]) error) TaskOption {
	return option{task: func(s *taskSettings) { s.hooks.onComplete = fn }}
}

// OnCancel is called, on a best-effort basis, when an attempt is cancelled
// while it runs. An error it returns is reported.
func OnCancel[P any](fn func(ctx context.Context, payload P) error) TaskOption {
	return option{task: func(s *taskSettings) { s.hooks.onCancel = fn }}
}

// CatchError is called when an attempt fails with an error other than
// [ErrAbort], and decides whether the run skips the attempts it has left.
func CatchError[P any](fn func(ctx context.Context, payload P, err error) (skipRetrying bool)) TaskOption {
	return option{task: func(s *taskSettings) { s.hooks.catchError = fn }}
}

func ensureBatchPolicy(policy **resourcesv1.BatchPolicy) *resourcesv1.BatchPolicy {
	if *policy == nil {
		*policy = &resourcesv1.BatchPolicy{}
	}
	return *policy
}

func newOptionalDuration(duration time.Duration) *durationpb.Duration {
	if duration == 0 {
		return nil
	}
	return durationpb.New(duration)
}
