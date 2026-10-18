// Package worker declares the workers an app's tasks and consumers run on,
// and serves the deliveries Ocel makes to them.
package worker

import (
	"context"

	"ocel.dev/internal/binding"
	"ocel.dev/internal/delivery"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
)

// An Option tunes the worker [New] declares.
type Option interface {
	applyWorker(*delivery.WorkerSettings)
}

type option func(*delivery.WorkerSettings)

func (o option) applyWorker(s *delivery.WorkerSettings) { o(s) }

// OnStart is called once per worker process, before its first run. While it
// fails, every delivery fails and is retried, and the next delivery calls it
// again.
func OnStart(fn func(ctx context.Context) error) Option {
	return option(func(s *delivery.WorkerSettings) { s.OnStart = fn })
}

// Concurrency is how many runs or messages the worker handles at once, at
// most, across everything it serves, 1 to 1000.
func Concurrency(limit int) Option {
	return option(func(s *delivery.WorkerSettings) { s.Concurrency = limit })
}

// Middleware wraps every run of everything the worker serves. It calls next
// to go on with the run, and returns its error or one of its own.
func Middleware(fn func(ctx context.Context, next func(context.Context) error) error) Option {
	return option(func(s *delivery.WorkerSettings) { s.Middleware = fn })
}

// A Worker is a worker an app declares: the compute that runs the tasks and
// consumers placed on it with the UseWorker option of [ocel.dev/task] and
// [ocel.dev/topic].
type Worker struct {
	name string
}

// New declares a worker named name and returns the handle UseWorker places
// tasks and consumers on. A worker joins the app of the same name in
// ocel.json. Tasks and consumers given no worker run on the one named
// "worker".
func New(name string, opts ...Option) *Worker {
	settings := delivery.WorkerSettings{}
	for _, opt := range opts {
		opt.applyWorker(&settings)
	}
	binding.Declare(binding.CallSite(0), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_WORKER, Name: name},
		Config: &resourcesv1.DeclareRequest_Worker{Worker: &resourcesv1.WorkerConfig{
			Concurrency: int32(settings.Concurrency),
		}},
	})
	delivery.RegisterWorker(name, settings)
	return &Worker{name: name}
}

// Name is the name the worker was declared under.
func (w *Worker) Name() string { return w.name }
