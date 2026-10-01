package ocel_test

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
)

type image struct {
	Key string `json:"key"`
}

type thumbnail struct {
	URL string `json:"url"`
}

func resize(context.Context, image) (thumbnail, error) { return thumbnail{}, nil }

func TestATaskDeclaresEveryOptionItIsGivenWhereItIsWritten(t *testing.T) {
	seen := discoveryDeclarations(t)
	media := ocel.Worker("media-tasks")

	_, file, line, _ := runtime.Caller(0)
	ocel.Task("resize-declared", resize,
		ocel.Schema(`{"type":"object"}`),
		ocel.Retry(ocel.RetryPolicy{MaxAttempts: 4, MinDelay: time.Second, MaxDelay: 30 * time.Second}),
		ocel.Concurrency(10),
		ocel.MaxDuration(5*time.Minute),
		ocel.TTL(time.Hour),
		ocel.Ordered(),
		ocel.UseWorker(media),
		ocel.Cron("0 * * * *"),
		ocel.OnSuccess(func(context.Context, image, thumbnail) error { return nil }),
		ocel.OnFailure(func(context.Context, image, error) error { return nil }),
		ocel.OnComplete(func(context.Context, image, ocel.RunResult[thumbnail]) error { return nil }),
		ocel.OnCancel(func(context.Context, image) error { return nil }),
		ocel.CatchError(func(context.Context, image, error) bool { return false }),
		ocel.OnStartAttempt(func(context.Context, image) error { return nil }),
		ocel.Middleware(func(ctx context.Context, next func(context.Context) error) error { return next(ctx) }),
	)

	decl := declared(t, *seen, "RESOURCE_TYPE_TASK", "resize-declared")
	if want := fmt.Sprintf("%s:%d", file, line+1); decl["source"] != want {
		t.Errorf("source = %v, want %q", decl["source"], want)
	}
	task, _ := decl["task"].(map[string]any)
	if task["schema"] != `{"type":"object"}` || task["concurrency"] != float64(10) || task["maxDuration"] != "300s" ||
		task["ttl"] != "3600s" || task["ordered"] != true || task["worker"] != "media-tasks" || task["cron"] != "0 * * * *" {
		t.Errorf("task = %v", task)
	}
	if retry, _ := task["retry"].(map[string]any); retry["maxAttempts"] != float64(4) || retry["minDelay"] != "1s" || retry["maxDelay"] != "30s" {
		t.Errorf("retry = %v", task["retry"])
	}
	if task["batch"] != nil {
		t.Errorf("batch = %v, want none on a task of single runs", task["batch"])
	}
}

func TestATaskDeclaredWithoutOptionsLeavesEveryOptionUnset(t *testing.T) {
	seen := discoveryDeclarations(t)

	ocel.Task("resize-bare", resize)

	if task, ok := declared(t, *seen, "RESOURCE_TYPE_TASK", "resize-bare")["task"].(map[string]any); !ok || len(task) != 0 {
		t.Errorf("task = %v, want an empty config", task)
	}
}

func TestABatchTaskDeclaresItsBatch(t *testing.T) {
	seen := discoveryDeclarations(t)

	ocel.BatchTask("resize-batched", 100, func(context.Context, []image) (int, error) { return 0, nil },
		ocel.BatchTimeout(5*time.Second))

	task, _ := declared(t, *seen, "RESOURCE_TYPE_TASK", "resize-batched")["task"].(map[string]any)
	if batch, _ := task["batch"].(map[string]any); batch["size"] != float64(100) || batch["timeout"] != "5s" {
		t.Errorf("batch = %v", task["batch"])
	}
}

func TestATaskGivenAHookOfAnotherPayloadTypePanicsNamingTheTypeItTakes(t *testing.T) {
	discoveryDeclarations(t)

	defer func() {
		message := fmt.Sprint(recover())
		if !strings.Contains(message, "OnSuccess") || !strings.Contains(message, "func(context.Context, ocel_test.image, ocel_test.thumbnail) error") {
			t.Errorf("panic = %q, want it to name the hook and the type it takes", message)
		}
	}()
	ocel.Task("resize-mistyped", resize, ocel.OnSuccess(func(context.Context, order, thumbnail) error { return nil }))
}

func deliveredTask(name string) map[string]string {
	return map[string]string{"OCEL_RESOURCE_TASK_" + name: fmt.Sprintf(`{"name":%q,"task":{}}`, name)}
}

func TestTriggerStartsARunOfTheTaskByItsDeclaredName(t *testing.T) {
	runtime := serveRuntime(t, deliveredTask("resize-trigger"))
	task := ocel.Task("resize-trigger", resize)

	run, err := task.Trigger(t.Context(), image{Key: "a.png"})
	if err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}

	req := only[*taskv1.TriggerRequest](t, runtime)
	if run != (ocel.RunHandle{ID: "run-1"}) || req.GetTask() != "resize-trigger" || string(req.GetPayload()) != `{"key":"a.png"}` {
		t.Errorf("Trigger() = %+v after %v", run, req)
	}
}

func TestTriggerNamesTheDeclaredTaskWhenItsBindingNamesAnotherAndSendsTheJSONTextUnchanged(t *testing.T) {
	runtime := serveRuntime(t, map[string]string{"OCEL_RESOURCE_TASK_measure-trigger": `{"name":"physical-measure-trigger","task":{}}`})
	task := ocel.Task("measure-trigger", func(_ context.Context, in json.RawMessage) (json.RawMessage, error) { return in, nil })

	if _, err := task.Trigger(t.Context(), json.RawMessage(exactJSON)); err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}

	req := only[*taskv1.TriggerRequest](t, runtime)
	if req.GetTask() != "measure-trigger" || string(req.GetPayload()) != exactJSON {
		t.Errorf("request = task %q payload %s, want the declared measure-trigger and %s", req.GetTask(), req.GetPayload(), exactJSON)
	}
}

func TestTriggerCarriesEveryOptionItIsGiven(t *testing.T) {
	runtime := serveRuntime(t, deliveredTask("resize-options"))
	task := ocel.Task("resize-options", resize)
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	if _, err := task.Trigger(t.Context(), image{},
		ocel.DelayUntil(at),
		ocel.TTL(2*time.Hour),
		ocel.IdempotencyKey("idem"),
		ocel.IdempotencyKeyTTL(24*time.Hour),
		ocel.Debounce("user-1", 10*time.Second),
		ocel.Key("user-1"),
		ocel.LaneHigh,
		ocel.MaxAttempts(2),
		ocel.Tags("user:1", "plan:pro"),
		ocel.RunMetadata(map[string]any{"source": "upload", "retries": 2}),
	); err != nil {
		t.Fatalf("Trigger() error = %v", err)
	}

	options := only[*taskv1.TriggerRequest](t, runtime).GetOptions()
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
	runtime := serveRuntime(t, deliveredTask("resize-batch-trigger"))
	task := ocel.Task("resize-batch-trigger", resize)

	runs, err := task.BatchTrigger(t.Context(),
		ocel.Trigger[image]{Payload: image{Key: "a"}},
		ocel.Trigger[image]{Payload: image{Key: "b"}, Options: []ocel.TriggerOption{ocel.Key("k")}},
	)
	if err != nil {
		t.Fatalf("BatchTrigger() error = %v", err)
	}

	req := only[*taskv1.BatchTriggerRequest](t, runtime)
	if !slices.Equal(runs, []ocel.RunHandle{{ID: "run-1"}, {ID: "run-2"}}) || req.GetTask() != "resize-batch-trigger" || len(req.GetItems()) != 2 {
		t.Fatalf("BatchTrigger() = %v after %v", runs, req)
	}
	if string(req.GetItems()[0].GetPayload()) != `{"key":"a"}` || req.GetItems()[1].GetOptions().GetKey() != "k" {
		t.Errorf("items = %v", req.GetItems())
	}
}

func TestTaskOperationsRefuseDuringDiscovery(t *testing.T) {
	discoveryDeclarations(t)
	task := ocel.Task("resize-in-discovery", resize)

	for access, err := range map[string]error{
		"Trigger":      second(task.Trigger(t.Context(), image{})),
		"BatchTrigger": second(task.BatchTrigger(t.Context(), ocel.Trigger[image]{})),
	} {
		var unprovisioned *ocel.UnprovisionedError
		if !errors.As(err, &unprovisioned) || unprovisioned.Resource != `task("resize-in-discovery")` || unprovisioned.Access != access {
			t.Errorf("%s error = %v, want an *UnprovisionedError naming the task", access, err)
		}
	}
}

func TestATaskBindingDeliveredForATopicIsRefusedNamingBothKinds(t *testing.T) {
	serveRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_resize": deliveredTask("resize")["OCEL_RESOURCE_TASK_resize"]})

	_, err := ocel.Topic[image]("resize").Send(t.Context(), image{})

	if err == nil || !strings.Contains(err.Error(), "contains a TASK binding, and this app reads it as a TOPIC") {
		t.Errorf("Send() error = %v", err)
	}
}
