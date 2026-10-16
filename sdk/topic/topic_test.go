package topic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"ocel.dev"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
	"ocel.dev/internal/sdktest"
	"ocel.dev/topic"
	"ocel.dev/worker"
)

type order struct {
	ID    string `json:"id"`
	Total int    `json:"total"`
}

func TestATopicDeclaresItsSchemaOrderAndRetryWhereItIsWritten(t *testing.T) {
	seen := sdktest.Declarations(t)

	_, file, line, _ := runtime.Caller(0)
	orders := topic.New[order]("orders-declared", topic.Schema(`{"type":"object"}`), topic.Ordered(),
		topic.Retry{MaxAttempts: 5, MinDelay: 2 * time.Second, MaxDelay: time.Minute})

	if orders.Name() != "orders-declared" {
		t.Errorf("Name() = %q", orders.Name())
	}
	if len(*seen) != 1 {
		t.Fatalf("declarations = %d, want 1", len(*seen))
	}
	decl := sdktest.Declared(t, *seen, "RESOURCE_TYPE_TOPIC", "orders-declared")
	if want := fmt.Sprintf("%s:%d", file, line+1); decl["source"] != want {
		t.Errorf("source = %v, want %q", decl["source"], want)
	}
	topic, _ := decl["topic"].(map[string]any)
	if topic["schema"] != `{"type":"object"}` || topic["ordered"] != true {
		t.Errorf("topic = %v", topic)
	}
	retry, _ := topic["retry"].(map[string]any)
	if retry["maxAttempts"] != float64(5) || retry["minDelay"] != "2s" || retry["maxDelay"] != "60s" {
		t.Errorf("retry = %v", retry)
	}
}

func TestATopicDeclaredWithoutOptionsLeavesEveryOptionUnset(t *testing.T) {
	seen := sdktest.Declarations(t)

	topic.New[order]("orders-bare")

	config, ok := sdktest.Declared(t, *seen, "RESOURCE_TYPE_TOPIC", "orders-bare")["topic"].(map[string]any)
	if !ok || len(config) != 0 {
		t.Errorf("topic = %v, want an empty config", config)
	}
}

func TestAConsumerDeclaresItsTopicWorkerAndLimits(t *testing.T) {
	seen := sdktest.Declarations(t)

	media := worker.New("media-consumers", worker.Concurrency(4))
	orders := topic.New[order]("orders-consumed")
	_, file, line, _ := runtime.Caller(0)
	orders.Consumer("audit", func(context.Context, order) error { return nil },
		topic.UseWorker(media),
		topic.Retry{MaxAttempts: 2},
		topic.Concurrency(8),
		topic.MaxDuration(90*time.Second),
		topic.Lanes(topic.LaneHigh, topic.LaneLow),
	)

	declaredWorker, _ := sdktest.Declared(t, *seen, "RESOURCE_TYPE_WORKER", "media-consumers")["worker"].(map[string]any)
	if declaredWorker["concurrency"] != float64(4) {
		t.Errorf("worker = %v", declaredWorker)
	}
	decl := sdktest.Declared(t, *seen, "RESOURCE_TYPE_CONSUMER", "audit")
	if want := fmt.Sprintf("%s:%d", file, line+1); decl["source"] != want {
		t.Errorf("source = %v, want %q", decl["source"], want)
	}
	consumer, _ := decl["consumer"].(map[string]any)
	if consumer["topic"] != "orders-consumed" || consumer["worker"] != "media-consumers" ||
		consumer["concurrency"] != float64(8) || consumer["maxDuration"] != "90s" {
		t.Errorf("consumer = %v", consumer)
	}
	if retry, _ := consumer["retry"].(map[string]any); retry["maxAttempts"] != float64(2) || retry["minDelay"] != nil {
		t.Errorf("retry = %v, want only maxAttempts set", retry)
	}
	if lanes, _ := consumer["lanes"].([]any); len(lanes) != 2 || lanes[0] != "LANE_HIGH" || lanes[1] != "LANE_LOW" {
		t.Errorf("lanes = %v", consumer["lanes"])
	}
	if consumer["batch"] != nil {
		t.Errorf("batch = %v, want none on a consumer of single messages", consumer["batch"])
	}
}

func TestABatchConsumerDeclaresItsBatchSizeAndTimeout(t *testing.T) {
	seen := sdktest.Declarations(t)

	orders := topic.New[order]("orders-batched")
	orders.BatchConsumer("ledger", 50, func(context.Context, []order) error { return nil }, topic.BatchTimeout(0))
	orders.BatchConsumer("ledger-defaults", 25, func(context.Context, []order) error { return nil })

	consumer, _ := sdktest.Declared(t, *seen, "RESOURCE_TYPE_CONSUMER", "ledger")["consumer"].(map[string]any)
	if batch, _ := consumer["batch"].(map[string]any); batch["size"] != float64(50) || batch["timeout"] != "0s" {
		t.Errorf("batch = %v", consumer["batch"])
	}
	if consumer["worker"] != nil {
		t.Errorf("worker = %v, want it left to the default worker", consumer["worker"])
	}
	defaults, _ := sdktest.Declared(t, *seen, "RESOURCE_TYPE_CONSUMER", "ledger-defaults")["consumer"].(map[string]any)
	if batch, _ := defaults["batch"].(map[string]any); batch["size"] != float64(25) || batch["timeout"] != nil {
		t.Errorf("batch = %v, want its size and the timeout left to the build", defaults["batch"])
	}
}

func TestABatchOptionOnAConsumerOfSingleMessagesPanics(t *testing.T) {
	sdktest.Declarations(t)
	orders := topic.New[order]("orders-misbatched")

	defer func() {
		if recover() == nil {
			t.Error("Consumer did not panic")
		}
	}()
	orders.Consumer("single", func(context.Context, order) error { return nil }, topic.BatchTimeout(time.Second))
}

func TestSendPublishesTheJSONPayloadToTheTopicByItsDeclaredName(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, sdktest.TopicBinding("orders"))
	orders := topic.New[order]("orders")

	id, err := orders.Send(t.Context(), order{ID: "o-1", Total: 30})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if id != "01HZY4B6Q0Z0Z0Z0Z0Z0Z0Z0Z0" {
		t.Errorf("Send() = %q, want the message id the runtime answered", id)
	}
	req := sdktest.Only[*topicv1.SendRequest](t, runtime)
	if req.GetTopic() != "orders" {
		t.Errorf("topic = %q, want the declared name", req.GetTopic())
	}
	if string(req.GetPayload()) != `{"id":"o-1","total":30}` {
		t.Errorf("payload = %s", req.GetPayload())
	}
	if req.GetDueAt() != nil || req.GetLane() != topicv1.Lane_LANE_UNSPECIFIED || req.GetKey() != "" {
		t.Errorf("request = %v, want no option set", req)
	}
}

func TestSendNamesTheDeclaredTopicWhenItsBindingNamesAnotherAndSendsTheJSONTextUnchanged(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_measures": `{"name":"physical-measures","topic":{}}`})

	if _, err := topic.New[json.RawMessage]("measures").Send(t.Context(), json.RawMessage(sdktest.ExactJSON)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	req := sdktest.Only[*topicv1.SendRequest](t, runtime)
	if req.GetTopic() != "measures" || string(req.GetPayload()) != sdktest.ExactJSON {
		t.Errorf("request = topic %q payload %s, want the declared measures and %s", req.GetTopic(), req.GetPayload(), sdktest.ExactJSON)
	}
}

func TestSendCarriesItsDelayIdempotencyKeyKeyAndLane(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, sdktest.TopicBinding("orders"))
	orders := topic.New[order]("orders")

	before := time.Now()
	if _, err := orders.Send(t.Context(), order{ID: "o-2"},
		topic.Delay(time.Hour), topic.IdempotencyKey("idem"), topic.Key("customer-9"), topic.LaneLow); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	req := sdktest.Only[*topicv1.SendRequest](t, runtime)
	due := req.GetDueAt().AsTime()
	if due.Before(before.Add(time.Hour)) || due.After(time.Now().Add(time.Hour)) {
		t.Errorf("dueAt = %v, want an hour from when Send was called", due)
	}
	if req.GetIdempotencyKey() != "idem" || req.GetKey() != "customer-9" || req.GetLane() != topicv1.Lane_LANE_LOW {
		t.Errorf("request = %v", req)
	}
}

func TestSendDueAtATimeCarriesThatTime(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, sdktest.TopicBinding("orders"))
	orders := topic.New[order]("orders")
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	if _, err := orders.Send(t.Context(), order{}, topic.DelayUntil(at)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if due := sdktest.Only[*topicv1.SendRequest](t, runtime).GetDueAt().AsTime(); !due.Equal(at) {
		t.Errorf("dueAt = %v, want %v", due, at)
	}
}

func TestSendRefusesAPayloadOver256KiBBeforeReachingTheRuntime(t *testing.T) {
	runtime := sdktest.ServeRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_notes": `{"name":"notes","topic":{}}`})
	notes := topic.New[string]("notes")

	_, err := notes.Send(t.Context(), strings.Repeat("a", 256<<10))

	if err == nil || !strings.Contains(err.Error(), "262144 bytes") {
		t.Errorf("Send() error = %v, want it to name the 262144-byte limit", err)
	}
	if received := runtime.Requests(); len(received) != 0 {
		t.Errorf("the runtime received %v, want nothing", sdktest.Names(received))
	}
}

func TestTopicOperationsRefuseDuringDiscovery(t *testing.T) {
	sdktest.Declarations(t)
	orders := topic.New[order]("orders-in-discovery")
	letters := orders.DeadLetter("audit")

	for access, err := range map[string]error{
		"Send":                        sdktest.ErrorOf(orders.Send(t.Context(), order{})),
		`DeadLetter("audit").List`:    sdktest.ErrorOf(letters.List(t.Context())),
		`DeadLetter("audit").Redrive`: sdktest.ErrorOf(letters.Redrive(t.Context())),
		`DeadLetter("audit").Purge`:   sdktest.ErrorOf(letters.Purge(t.Context())),
		`DeadLetter("audit").Count`:   sdktest.ErrorOf(letters.Count(t.Context())),
	} {
		var unprovisioned *ocel.UnprovisionedError
		if !errors.As(err, &unprovisioned) {
			t.Fatalf("%s error = %v, want an *UnprovisionedError", access, err)
		}
		if unprovisioned.Resource != `topic("orders-in-discovery")` || unprovisioned.Access != access {
			t.Errorf("error = %+v, want it to name the topic and %s", unprovisioned, access)
		}
	}
}

func TestSendConnectsOnceTheBindingArrivesAfterAFailedSend(t *testing.T) {
	orders := topic.New[order]("orders")

	var missing *ocel.MissingBindingError
	if _, err := orders.Send(t.Context(), order{ID: "o-1"}); !errors.As(err, &missing) {
		t.Fatalf("Send() before the binding err = %v, want a *ocel.MissingBindingError", err)
	}
	sdktest.ServeRuntime(t, sdktest.TopicBinding("orders"))

	if _, err := orders.Send(t.Context(), order{ID: "o-1"}); err != nil {
		t.Errorf("Send() after the binding arrived = %v, want the topic connected", err)
	}
}
