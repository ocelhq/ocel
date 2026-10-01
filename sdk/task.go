package ocel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	taskv1 "ocel.dev/internal/proto/app/task/v1"
	"ocel.dev/internal/proto/app/task/v1/taskv1connect"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

// A TaskDefinition is a task an app declares: work triggered with a P, run on
// a worker, that returns an R.
type TaskDefinition[P, R any] struct {
	name       string
	connection boundResourceConnection[taskv1connect.TaskServiceClient]
}

// A RunHandle is a run a trigger started.
type RunHandle struct {
	// ID is the run's id, which [RetrieveRun] and the other run functions
	// take.
	ID string
}

// Task declares a task named name that runs run once per trigger, on the
// worker [UseWorker] names or the default one. An error run returns fails the
// attempt, and it is retried unless the error wraps [ErrAbort]. Call it from a
// file under the project's discovery folder: during discovery the call is the
// declaration, and at runtime it reads the binding the deploy delivered for
// that name.
//
// The hooks it takes are typed by the task: [OnSuccess] takes a
// func(context.Context, P, R) error. Task panics when one is not.
func Task[P, R any](name string, run func(ctx context.Context, payload P) (R, error), opts ...TaskOption) *TaskDefinition[P, R] {
	settings := newTaskSettings(opts)
	if settings.config.Batch != nil {
		panic(fmt.Sprintf("ocel: task %q runs one payload at a time, so it batches nothing: declare it with BatchTask", name))
	}
	hooks := mustAssertHookTypes[P, R](name, settings.hooks)
	declareTask(readCallSite(), name, settings)
	registerRoute(name, name, &route{
		kind:   KindTask,
		name:   name,
		worker: resolveWorkerName(settings.config.GetWorker()),
		serve:  newSingleServeFunc(work[P, R]{kind: KindTask, name: name, run: run, hooks: hooks}),
	})
	return &TaskDefinition[P, R]{name: name}
}

// BatchTask declares a task named name whose runs are delivered in batches of
// at most size, up to 1000, or 10 when ordered, and bounded in time by
// [BatchTimeout]: run is handed every payload of a batch at once, and its
// outcome is every run's. Each trigger still takes one P.
//
// The hooks it takes are typed by the batch: [OnSuccess] takes a
// func(context.Context, []P, R) error. BatchTask panics when one is not.
func BatchTask[P, R any](name string, size int, run func(ctx context.Context, payloads []P) (R, error), opts ...TaskOption) *TaskDefinition[P, R] {
	settings := newTaskSettings(opts)
	ensureBatchPolicy(&settings.config.Batch).Size = int32(size)
	hooks := mustAssertHookTypes[[]P, R](name, settings.hooks)
	declareTask(readCallSite(), name, settings)
	registerRoute(name, name, &route{
		kind:    KindTask,
		name:    name,
		worker:  resolveWorkerName(settings.config.GetWorker()),
		batched: true,
		serve:   newBatchServeFunc(work[[]P, R]{kind: KindTask, name: name, run: run, hooks: hooks}),
	})
	return &TaskDefinition[P, R]{name: name}
}

func newTaskSettings(opts []TaskOption) taskSettings {
	settings := taskSettings{config: &resourcesv1.TaskConfig{}}
	for _, opt := range opts {
		opt.applyTask(&settings)
	}
	return settings
}

func declareTask(source, name string, settings taskSettings) {
	declareResource(source, &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: name},
		Config:   &resourcesv1.DeclareRequest_Task{Task: settings.config},
	})
}

// Name is the name the task was declared under.
func (t *TaskDefinition[P, R]) Name() string { return t.name }

// A Trigger is one run [TaskDefinition.BatchTrigger] starts: its payload, and
// the options that run alone takes.
type Trigger[P any] struct {
	// Payload is what the run is handed.
	Payload P
	// Options tune this run alone.
	Options []TriggerOption
}

// Trigger starts a run of the task with payload and returns its handle.
func (t *TaskDefinition[P, R]) Trigger(ctx context.Context, payload P, opts ...TriggerOption) (RunHandle, error) {
	task, err := t.connect("Trigger")
	if err != nil {
		return RunHandle{}, err
	}
	body, err := encodePayload(payload)
	if err != nil {
		return RunHandle{}, err
	}
	options, err := encodeTriggerOptions(opts)
	if err != nil {
		return RunHandle{}, err
	}
	res, err := task.client.Trigger(ctx, &taskv1.TriggerRequest{Task: t.name, Payload: body, Options: options})
	if err != nil {
		return RunHandle{}, err
	}
	return RunHandle{ID: res.GetId()}, nil
}

// BatchTrigger starts one run per trigger, up to 1000, and returns their
// handles in the same order.
func (t *TaskDefinition[P, R]) BatchTrigger(ctx context.Context, triggers ...Trigger[P]) ([]RunHandle, error) {
	task, err := t.connect("BatchTrigger")
	if err != nil {
		return nil, err
	}
	req := &taskv1.BatchTriggerRequest{Task: t.name}
	for i, trigger := range triggers {
		body, err := encodePayload(trigger.Payload)
		if err != nil {
			return nil, fmt.Errorf("trigger %d: %w", i, err)
		}
		options, err := encodeTriggerOptions(trigger.Options)
		if err != nil {
			return nil, fmt.Errorf("trigger %d: %w", i, err)
		}
		req.Items = append(req.Items, &taskv1.BatchTriggerItem{Payload: body, Options: options})
	}
	res, err := task.client.BatchTrigger(ctx, req)
	if err != nil {
		return nil, err
	}
	handles := make([]RunHandle, 0, len(res.GetIds()))
	for _, id := range res.GetIds() {
		handles = append(handles, RunHandle{ID: id})
	}
	return handles, nil
}

func (t *TaskDefinition[P, R]) connect(access string) (*boundResource[taskv1connect.TaskServiceClient], error) {
	return t.connection.connect(fmt.Sprintf("task(%q)", t.name), access, func() (*boundResource[taskv1connect.TaskServiceClient], error) {
		return dialBoundResource(t.name, bindingsv1.BindingType_BINDING_TYPE_TASK, taskv1connect.NewTaskServiceClient)
	})
}

func newTaskClient() (taskv1connect.TaskServiceClient, error) {
	address, authorized, err := readRuntimeConnection()
	if err != nil {
		return nil, err
	}
	return taskv1connect.NewTaskServiceClient(http.DefaultClient, address, authorized), nil
}

func encodeTriggerOptions(opts []TriggerOption) (*taskv1.TriggerOptions, error) {
	settings := triggerSettings{}
	for _, opt := range opts {
		opt.applyTrigger(&settings)
	}
	options := &taskv1.TriggerOptions{
		DueAt:             settings.encodeDueAt(),
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
		raw, err := encodeJSON(settings.metadata)
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
