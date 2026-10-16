// Package task declares tasks: work an app triggers with a payload, which Ocel
// runs on a worker and retries until it succeeds or runs out of attempts.
package task

import (
	"context"
	"encoding/json"
	"fmt"

	"ocel.dev/internal/binding"
	"ocel.dev/internal/delivery"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	taskv1 "ocel.dev/internal/proto/app/task/v1"
	"ocel.dev/internal/proto/app/task/v1/taskv1connect"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
	"ocel.dev/runs"
)

// A Task is a task an app declares: work triggered with a P, run on a worker,
// that returns an R.
type Task[P, R any] struct {
	name       string
	connection binding.Connection[taskv1connect.TaskServiceClient]
}

// New declares a task named name that runs run once per trigger, on the
// worker [UseWorker] names or the default one. An error run returns fails the
// attempt, and it is retried unless the error wraps
// [ocel.dev/runs.ErrAbort]. Call it from a file under the project's discovery
// folder: during discovery the call is the declaration, and at runtime it
// reads the binding the deploy delivered for that name.
//
// The hooks it takes are typed by the task: [OnSuccess] takes a
// func(context.Context, P, R) error. New panics when one is not.
func New[P, R any](name string, run func(ctx context.Context, payload P) (R, error), opts ...Option) *Task[P, R] {
	settings := newSettings(opts)
	if settings.config.Batch != nil {
		panic(fmt.Sprintf("ocel: task %q runs one payload at a time, so it batches nothing: declare it with NewBatch", name))
	}
	hooks := mustAssertHookTypes[P, R](name, settings.hooks)
	declare(binding.ReadCallSite(), name, settings)
	delivery.RegisterRoute(name, name, &delivery.Route{
		Kind:   runs.KindTask,
		Name:   name,
		Worker: delivery.ResolveWorkerName(settings.config.GetWorker()),
		Serve:  delivery.NewSingleServeFunc(delivery.Work[P, R]{Kind: runs.KindTask, Name: name, Run: run, Hooks: hooks}),
	})
	return &Task[P, R]{name: name}
}

// NewBatch declares a task named name whose runs are delivered in batches of
// at most size, up to 1000, or 10 when ordered, and bounded in time by
// [BatchTimeout]: run is handed every payload of a batch at once, and its
// outcome is every run's. Each trigger still takes one P.
//
// The hooks it takes are typed by the batch: [OnSuccess] takes a
// func(context.Context, []P, R) error. NewBatch panics when one is not.
func NewBatch[P, R any](name string, size int, run func(ctx context.Context, payloads []P) (R, error), opts ...Option) *Task[P, R] {
	settings := newSettings(opts)
	delivery.EnsureBatchPolicy(&settings.config.Batch).Size = int32(size)
	hooks := mustAssertHookTypes[[]P, R](name, settings.hooks)
	declare(binding.ReadCallSite(), name, settings)
	delivery.RegisterRoute(name, name, &delivery.Route{
		Kind:    runs.KindTask,
		Name:    name,
		Worker:  delivery.ResolveWorkerName(settings.config.GetWorker()),
		Batched: true,
		Serve:   delivery.NewBatchServeFunc(delivery.Work[[]P, R]{Kind: runs.KindTask, Name: name, Run: run, Hooks: hooks}),
	})
	return &Task[P, R]{name: name}
}

func newSettings(opts []Option) settings {
	s := settings{config: &resourcesv1.TaskConfig{}}
	for _, opt := range opts {
		opt.applyTask(&s)
	}
	return s
}

func declare(source, name string, s settings) {
	binding.DeclareResource(source, &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: name},
		Config:   &resourcesv1.DeclareRequest_Task{Task: s.config},
	})
}

// Name is the name the task was declared under.
func (t *Task[P, R]) Name() string { return t.name }

// A Trigger is one run [Task.BatchTrigger] starts: its payload, and the
// options that run alone takes.
type Trigger[P any] struct {
	// Payload is what the run is handed.
	Payload P
	// Options tune this run alone.
	Options []TriggerOption
}

// Trigger starts a run of the task with payload and returns its handle.
func (t *Task[P, R]) Trigger(ctx context.Context, payload P, opts ...TriggerOption) (runs.Handle, error) {
	task, err := t.connect("Trigger")
	if err != nil {
		return runs.Handle{}, err
	}
	body, err := binding.EncodePayload(payload)
	if err != nil {
		return runs.Handle{}, err
	}
	options, err := encodeTriggerOptions(opts)
	if err != nil {
		return runs.Handle{}, err
	}
	res, err := task.Client.Trigger(ctx, &taskv1.TriggerRequest{Task: t.name, Payload: body, Options: options})
	if err != nil {
		return runs.Handle{}, err
	}
	return runs.Handle{ID: res.GetId()}, nil
}

// BatchTrigger starts one run per trigger, up to 1000, and returns their
// handles in the same order.
func (t *Task[P, R]) BatchTrigger(ctx context.Context, triggers ...Trigger[P]) ([]runs.Handle, error) {
	task, err := t.connect("BatchTrigger")
	if err != nil {
		return nil, err
	}
	req := &taskv1.BatchTriggerRequest{Task: t.name}
	for i, trigger := range triggers {
		body, err := binding.EncodePayload(trigger.Payload)
		if err != nil {
			return nil, fmt.Errorf("trigger %d: %w", i, err)
		}
		options, err := encodeTriggerOptions(trigger.Options)
		if err != nil {
			return nil, fmt.Errorf("trigger %d: %w", i, err)
		}
		req.Items = append(req.Items, &taskv1.BatchTriggerItem{Payload: body, Options: options})
	}
	res, err := task.Client.BatchTrigger(ctx, req)
	if err != nil {
		return nil, err
	}
	handles := make([]runs.Handle, 0, len(res.GetIds()))
	for _, id := range res.GetIds() {
		handles = append(handles, runs.Handle{ID: id})
	}
	return handles, nil
}

func (t *Task[P, R]) connect(access string) (*binding.Resource[taskv1connect.TaskServiceClient], error) {
	return t.connection.Connect(fmt.Sprintf("task(%q)", t.name), access, func() (*binding.Resource[taskv1connect.TaskServiceClient], error) {
		return binding.Dial(t.name, bindingsv1.BindingType_BINDING_TYPE_TASK, taskv1connect.NewTaskServiceClient)
	})
}

func encodeTriggerOptions(opts []TriggerOption) (*taskv1.TriggerOptions, error) {
	settings := triggerSettings{}
	for _, opt := range opts {
		opt.applyTrigger(&settings)
	}
	options := &taskv1.TriggerOptions{
		DueAt:             delivery.EncodeDueAt(settings.due),
		Ttl:               settings.ttl,
		IdempotencyKey:    settings.idempotencyKey,
		IdempotencyKeyTtl: settings.idempotencyKeyTTL,
		Debounce:          settings.debounce,
		Key:               settings.key,
		Lane:              settings.lane,
		MaxAttempts:       settings.maxAttempts,
		Tags:              settings.tags,
	}
	if settings.metadata != nil {
		raw, err := binding.EncodeJSON(settings.metadata)
		if err != nil {
			return nil, fmt.Errorf("the run's metadata does not encode as JSON: %w", err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil || object == nil {
			return nil, fmt.Errorf("the run's metadata is not a JSON object: %s", raw)
		}
		options.Metadata = raw
	}
	return options, nil
}
