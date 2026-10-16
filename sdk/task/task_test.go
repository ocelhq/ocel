package task_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"ocel.dev"
	taskv1 "ocel.dev/internal/proto/app/task/v1"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
	"ocel.dev/internal/sdktest"
	"ocel.dev/runs"
	"ocel.dev/task"
	"ocel.dev/topic"
	"ocel.dev/worker"
)

type image struct {
	Key string `json:"key"`
}

type thumbnail struct {
	URL string `json:"url"`
}

func resize(context.Context, image) (thumbnail, error) { return thumbnail{}, nil }

type order struct {
	ID string `json:"id"`
}

func TestATaskDeclaresEveryOptionItIsGivenWhereItIsWritten(t *testing.T) {
	seen := sdktest.Declarations(t)
	media := worker.New("media-tasks")

	_, file, line, _ := runtime.Caller(0)
	task.New("resize-declared", resize,
		task.Schema(`{"type":"object"}`),
		task.Retry{MaxAttempts: 4, MinDelay: time.Second, MaxDelay: 30 * time.Second},
		task.Concurrency(10),
		task.MaxDuration(5*time.Minute),
		task.TTL(time.Hour),
		task.Ordered(),
		task.UseWorker(media),
		task.Cron("0 * * * *"),
		task.OnSuccess(func(context.Context, image, thumbnail) error { return nil }),
		task.OnFailure(func(context.Context, image, error) error { return nil }),
		task.OnComplete(func(context.Context, image, task.RunResult[thumbnail]) error { return nil }),
		task.OnCancel(func(context.Context, image) error { return nil }),
		task.CatchError(func(context.Context, image, error) bool { return false }),
		task.OnStartAttempt(func(context.Context, image) error { return nil }),
		task.Middleware(func(ctx context.Context, next func(context.Context) error) error { return next(ctx) }),
	)

	decl := sdktest.Declared(t, *seen, "RESOURCE_TYPE_TASK", "resize-declared")
	if want := fmt.Sprintf("%s:%d", file, line+1); decl["source"] != want {
		t.Errorf("source = %v, want %q", decl["source"], want)
	}
	config, _ := decl["task"].(map[string]any)
	if config["schema"] != `{"type":"object"}` || config["concurrency"] != float64(10) || config["maxDuration"] != "300s" ||
		config["ttl"] != "3600s" || config["ordered"] != true || config["worker"] != "media-tasks" || config["cron"] != "0 * * * *" {
		t.Errorf("task = %v", config)
	}
	if retry, _ := config["retry"].(map[string]any); retry["maxAttempts"] != float64(4) || retry["minDelay"] != "1s" || retry["maxDelay"] != "30s" {
		t.Errorf("retry = %v", config["retry"])
	}
	if config["batch"] != nil {
		t.Errorf("batch = %v, want none on a task of single runs", config["batch"])
	}
}

func TestATaskDeclaredWithoutOptionsLeavesEveryOptionUnset(t *testing.T) {
	seen := sdktest.Declarations(t)

	task.New("resize-bare", resize)

	if config, ok := sdktest.Declared(t, *seen, "RESOURCE_TYPE_TASK", "resize-bare")["task"].(map[string]any); !ok || len(config) != 0 {
		t.Errorf("task = %v, want an empty config", config)
	}
}

func TestABatchTaskDeclaresItsBatch(t *testing.T) {
	seen := sdktest.Declarations(t)

	task.NewBatch("resize-batched", 100, func(context.Context, []image) (int, error) { return 0, nil },
		task.BatchTimeout(5*time.Second))

	config, _ := sdktest.Declared(t, *seen, "RESOURCE_TYPE_TASK", "resize-batched")["task"].(map[string]any)
	if batch, _ := config["batch"].(map[string]any); batch["size"] != float64(100) || batch["timeout"] != "5s" {
		t.Errorf("batch = %v", config["batch"])
	}
}

func TestATaskGivenAHookOfAnotherPayloadTypePanicsNamingTheTypeItTakes(t *testing.T) {
	sdktest.Declarations(t)

	defer func() {
		message := fmt.Sprint(recover())
		if !strings.Contains(message, "OnSuccess") || !strings.Contains(message, "func(context.Context, task_test.image, task_test.thumbnail) error") {
			t.Errorf("panic = %q, want it to name the hook and the type it takes", message)
		}
	}()
	task.New("resize-mistyped", resize, task.OnSuccess(func(context.Context, order, thumbnail) error { return nil }))
}

func TestTriggerStartsARunOfTheTaskByItsDeclaredName(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, sdktest.TaskBinding("resize-trigger"))
	resizing := task.New("resize-trigger", resize)

	run, err := resizing.Trigger(t.Context(), image{Key: "a.png"})
	if err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}

	req := sdktest.Only[*taskv1.TriggerRequest](t, runtime)
	if run != (runs.Handle{ID: "run-1"}) || req.GetTask() != "resize-trigger" || string(req.GetPayload()) != `{"key":"a.png"}` {
		t.Errorf("Trigger() = %+v after %v", run, req)
	}
}

func TestTriggerNamesTheDeclaredTaskWhenItsBindingNamesAnotherAndSendsTheJSONTextUnchanged(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, map[string]string{"OCEL_RESOURCE_TASK_measure-trigger": `{"name":"physical-measure-trigger","task":{}}`})
	resizing := task.New("measure-trigger", func(_ context.Context, in json.RawMessage) (json.RawMessage, error) { return in, nil })

	if _, err := resizing.Trigger(t.Context(), json.RawMessage(sdktest.ExactJSON)); err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}

	req := sdktest.Only[*taskv1.TriggerRequest](t, runtime)
	if req.GetTask() != "measure-trigger" || string(req.GetPayload()) != sdktest.ExactJSON {
		t.Errorf("request = task %q payload %s, want the declared measure-trigger and %s", req.GetTask(), req.GetPayload(), sdktest.ExactJSON)
	}
}

func TestTriggerCarriesEveryOptionItIsGiven(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, sdktest.TaskBinding("resize-options"))
	resizing := task.New("resize-options", resize)
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	if _, err := resizing.Trigger(t.Context(), image{},
		task.DelayUntil(at),
		task.TTL(2*time.Hour),
		task.IdempotencyKey("idem"),
		task.IdempotencyKeyTTL(24*time.Hour),
		task.Debounce("user-1", 10*time.Second),
		task.Key("user-1"),
		task.LaneHigh,
		task.MaxAttempts(2),
		task.Tags("user:1", "plan:pro"),
		task.RunMetadata(map[string]any{"source": "upload", "retries": 2}),
	); err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}

	options := sdktest.Only[*taskv1.TriggerRequest](t, runtime).GetOptions()
	if !options.GetDueAt().AsTime().Equal(at) || options.GetTtl().AsDuration() != 2*time.Hour ||
		options.GetIdempotencyKey() != "idem" || options.GetIdempotencyKeyTtl().AsDuration() != 24*time.Hour ||
		options.GetDebounce().GetKey() != "user-1" || options.GetDebounce().GetDelay().AsDuration() != 10*time.Second ||
		options.GetKey() != "user-1" || options.GetLane() != topicv1.Lane_LANE_HIGH || options.GetMaxAttempts() != 2 ||
		!slices.Equal(options.GetTags(), []string{"user:1", "plan:pro"}) {
		t.Errorf("options = %v", options)
	}
	if metadata := string(options.GetMetadata()); metadata != `{"retries":2,"source":"upload"}` {
		t.Errorf("metadata = %s", metadata)
	}
}

func TestBatchTriggerStartsOneRunPerTriggerAndReturnsTheirHandlesInOrder(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, sdktest.TaskBinding("resize-batch-trigger"))
	resizing := task.New("resize-batch-trigger", resize)

	handles, err := resizing.BatchTrigger(t.Context(),
		task.Trigger[image]{Payload: image{Key: "a"}},
		task.Trigger[image]{Payload: image{Key: "b"}, Options: []task.TriggerOption{task.Key("k")}},
	)
	if err != nil {
		t.Fatalf("BatchTrigger() error = %v", err)
	}

	req := sdktest.Only[*taskv1.BatchTriggerRequest](t, runtime)
	if !slices.Equal(handles, []runs.Handle{{ID: "run-1"}, {ID: "run-2"}}) || req.GetTask() != "resize-batch-trigger" || len(req.GetItems()) != 2 {
		t.Fatalf("BatchTrigger() = %v after %v", handles, req)
	}
	if string(req.GetItems()[0].GetPayload()) != `{"key":"a"}` || req.GetItems()[1].GetOptions().GetKey() != "k" {
		t.Errorf("items = %v", req.GetItems())
	}
}

func TestTaskOperationsRefuseDuringDiscovery(t *testing.T) {
	sdktest.Declarations(t)
	resizing := task.New("resize-in-discovery", resize)

	for access, err := range map[string]error{
		"Trigger":      sdktest.ErrorOf(resizing.Trigger(t.Context(), image{})),
		"BatchTrigger": sdktest.ErrorOf(resizing.BatchTrigger(t.Context(), task.Trigger[image]{})),
	} {
		var unprovisioned *ocel.UnprovisionedError
		if !errors.As(err, &unprovisioned) || unprovisioned.Resource != `task("resize-in-discovery")` || unprovisioned.Access != access {
			t.Errorf("%s error = %v, want an *UnprovisionedError naming the task", access, err)
		}
	}
}

func TestATaskBindingDeliveredForATopicIsRefusedNamingBothKinds(t *testing.T) {
	sdktest.ServeRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_resize": sdktest.TaskBinding("resize")["OCEL_RESOURCE_TASK_resize"]})

	_, err := topic.New[image]("resize").Send(t.Context(), image{})

	if err == nil || !strings.Contains(err.Error(), "contains a TASK binding, and this app reads it as a TOPIC") {
		t.Errorf("Send() error = %v", err)
	}
}
