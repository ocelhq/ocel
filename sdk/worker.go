package ocel

import (
	"context"
	"fmt"
	"sync"

	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
)

const defaultWorker = "worker"

// A WorkerOption tunes the worker [Worker] declares.
type WorkerOption interface {
	applyWorker(*workerSettings)
}

type workerSettings struct {
	concurrency int
	onStart     func(context.Context) error
	middleware  middleware
}

// OnStart is called once per worker process, before its first run. While it
// fails, every delivery fails and is retried, and the next delivery calls it
// again.
func OnStart(fn func(ctx context.Context) error) WorkerOption {
	return option{worker: func(s *workerSettings) { s.onStart = fn }}
}

// A WorkerDefinition is a worker an app declares: the compute that runs the
// tasks and consumers placed on it with [UseWorker].
type WorkerDefinition struct {
	name     string
	settings workerSettings
	slots    chan struct{}

	mu       sync.Mutex
	started  bool
	starting *workerStartup
}

type workerStartup struct {
	done chan struct{}
	err  error
}

// Worker declares a worker named name and returns the handle [UseWorker]
// places tasks and consumers on. A worker joins the app of the same name in
// ocel.json. Tasks and consumers given no worker run on the one named
// "worker".
func Worker(name string, opts ...WorkerOption) *WorkerDefinition {
	settings := workerSettings{}
	for _, opt := range opts {
		opt.applyWorker(&settings)
	}
	declareResource(readCallSite(), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_WORKER, Name: name},
		Config: &resourcesv1.DeclareRequest_Worker{Worker: &resourcesv1.WorkerConfig{
			Concurrency: int32(settings.concurrency),
		}},
	})
	worker := newWorker(name, settings)
	registerWorker(worker)
	return worker
}

func newWorker(name string, settings workerSettings) *WorkerDefinition {
	worker := &WorkerDefinition{name: name, settings: settings}
	if settings.concurrency > 0 {
		worker.slots = make(chan struct{}, settings.concurrency)
	}
	return worker
}

// Name is the name the worker was declared under.
func (w *WorkerDefinition) Name() string { return w.name }

func (w *WorkerDefinition) start(ctx context.Context) error {
	if w.settings.onStart == nil {
		return nil
	}
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return nil
	}
	pending := w.starting
	first := pending == nil
	if first {
		pending = &workerStartup{done: make(chan struct{})}
		w.starting = pending
	}
	w.mu.Unlock()

	if !first {
		select {
		case <-pending.done:
			return pending.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	pending.err = catchPanic(func() error { return w.settings.onStart(context.WithoutCancel(ctx)) })
	if pending.err != nil {
		pending.err = fmt.Errorf("worker %q did not start: %w", w.name, pending.err)
	}
	w.mu.Lock()
	w.starting = nil
	w.started = pending.err == nil
	w.mu.Unlock()
	close(pending.done)
	return pending.err
}

func (w *WorkerDefinition) acquireSlot(ctx context.Context) (release func(), err error) {
	if w.slots == nil {
		return func() {}, nil
	}
	select {
	case w.slots <- struct{}{}:
		return func() { <-w.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *WorkerDefinition) wrapAttempt(ctx context.Context, next func(context.Context) error) error {
	if w.settings.middleware == nil {
		return next(ctx)
	}
	return w.settings.middleware(ctx, next)
}
