package delivery

import (
	"context"
	"fmt"
	"sync"
)

type WorkerSettings struct {
	Concurrency int
	OnStart     func(context.Context) error
	Middleware  Middleware
}

type worker struct {
	name     string
	settings WorkerSettings
	slots    chan struct{}

	mu       sync.Mutex
	started  bool
	starting *workerStartup
}

type workerStartup struct {
	done chan struct{}
	err  error
}

func newWorker(name string, settings WorkerSettings) *worker {
	w := &worker{name: name, settings: settings}
	if settings.Concurrency > 0 {
		w.slots = make(chan struct{}, settings.Concurrency)
	}
	return w
}

func (w *worker) start(ctx context.Context) error {
	if w.settings.OnStart == nil {
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
	pending.err = catchPanic(func() error { return w.settings.OnStart(context.WithoutCancel(ctx)) })
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

func (w *worker) acquireSlot(ctx context.Context) (release func(), err error) {
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

func (w *worker) wrapAttempt(ctx context.Context, next func(context.Context) error) error {
	if w.settings.Middleware == nil {
		return next(ctx)
	}
	return w.settings.Middleware(ctx, next)
}
