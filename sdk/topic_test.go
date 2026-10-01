package ocel_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"ocel.dev"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
)

type order struct {
	ID    string `json:"id"`
	Total int    `json:"total"`
}

func discoveryDeclarations(t *testing.T) *[]map[string]any {
	t.Helper()
	var seen []map[string]any
	srv := collector(t, &seen)
	t.Setenv("OCEL_PHASE", "discovery")
	t.Setenv("OCEL_DEV_SERVER", srv.URL)
	t.Setenv("OCEL_DEV_SERVER_TOKEN", collectorToken)
	return &seen
}

func declared(t *testing.T, seen []map[string]any, kind, name string) map[string]any {
	t.Helper()
	for _, decl := range seen {
		resource, _ := decl["resource"].(map[string]any)
		if resource["type"] == kind && resource["name"] == name {
			if decl["__path"] != "/app.resources.v1.ResourceService/Declare" {
				t.Errorf("%s %q was posted to %v", kind, name, decl["__path"])
			}
			return decl
		}
	}
	t.Fatalf("no %s named %q among the declarations %v", kind, name, seen)
	return nil
}

func TestATopicDeclaresItsSchemaOrderAndRetryWhereItIsWritten(t *testing.T) {
	seen := discoveryDeclarations(t)

	_, file, line, _ := runtime.Caller(0)
	orders := ocel.Topic[order]("orders-declared", ocel.Schema(`{"type":"object"}`), ocel.Ordered(),
		ocel.Retry(ocel.RetryPolicy{MaxAttempts: 5, MinDelay: 2 * time.Second, MaxDelay: time.Minute}))

	if orders.Name() != "orders-declared" {
		t.Errorf("Name() = %q", orders.Name())
	}
	if len(*seen) != 1 {
		t.Fatalf("declarations = %d, want 1", len(*seen))
	}
	decl := declared(t, *seen, "RESOURCE_TYPE_TOPIC", "orders-declared")
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
	seen := discoveryDeclarations(t)

	ocel.Topic[order]("orders-bare")

	topic, ok := declared(t, *seen, "RESOURCE_TYPE_TOPIC", "orders-bare")["topic"].(map[string]any)
	if !ok || len(topic) != 0 {
		t.Errorf("topic = %v, want an empty config", topic)
	}
}

func TestAConsumerDeclaresItsTopicWorkerAndLimits(t *testing.T) {
	seen := discoveryDeclarations(t)

	media := ocel.Worker("media-consumers", ocel.Concurrency(4))
	orders := ocel.Topic[order]("orders-consumed")
	_, file, line, _ := runtime.Caller(0)
	orders.Consumer("audit", func(context.Context, order) error { return nil },
		ocel.UseWorker(media),
		ocel.Retry(ocel.RetryPolicy{MaxAttempts: 2}),
		ocel.Concurrency(8),
		ocel.MaxDuration(90*time.Second),
		ocel.Lanes(ocel.LaneHigh, ocel.LaneLow),
	)

	worker, _ := declared(t, *seen, "RESOURCE_TYPE_WORKER", "media-consumers")["worker"].(map[string]any)
	if worker["concurrency"] != float64(4) {
		t.Errorf("worker = %v", worker)
	}
	decl := declared(t, *seen, "RESOURCE_TYPE_CONSUMER", "audit")
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
	seen := discoveryDeclarations(t)

	orders := ocel.Topic[order]("orders-batched")
	orders.BatchConsumer("ledger", 50, func(context.Context, []order) error { return nil }, ocel.BatchTimeout(0))
	orders.BatchConsumer("ledger-defaults", 25, func(context.Context, []order) error { return nil })

	consumer, _ := declared(t, *seen, "RESOURCE_TYPE_CONSUMER", "ledger")["consumer"].(map[string]any)
	if batch, _ := consumer["batch"].(map[string]any); batch["size"] != float64(50) || batch["timeout"] != "0s" {
		t.Errorf("batch = %v", consumer["batch"])
	}
	if consumer["worker"] != nil {
		t.Errorf("worker = %v, want it left to the default worker", consumer["worker"])
	}
	defaults, _ := declared(t, *seen, "RESOURCE_TYPE_CONSUMER", "ledger-defaults")["consumer"].(map[string]any)
	if batch, _ := defaults["batch"].(map[string]any); batch["size"] != float64(25) || batch["timeout"] != nil {
		t.Errorf("batch = %v, want its size and the timeout left to the build", defaults["batch"])
	}
}

func TestABatchOptionOnAConsumerOfSingleMessagesPanics(t *testing.T) {
	discoveryDeclarations(t)
	orders := ocel.Topic[order]("orders-misbatched")

	defer func() {
		if recover() == nil {
			t.Error("Consumer did not panic")
		}
	}()
	orders.Consumer("single", func(context.Context, order) error { return nil }, ocel.BatchTimeout(time.Second))
}

const boundTopic = `{"name":"orders","topic":{"topic":"project-env-orders"}}`

func TestSendPublishesTheJSONPayloadToTheTopicItsBindingNames(t *testing.T) {
	runtime := serveRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_orders": boundTopic})
	orders := ocel.Topic[order]("orders")

	id, err := orders.Send(t.Context(), order{ID: "o-1", Total: 30})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if id != "01HZY4B6Q0Z0Z0Z0Z0Z0Z0Z0Z0" {
		t.Errorf("Send() = %q, want the message id the runtime answered", id)
	}
	req := only[*topicv1.SendRequest](t, runtime)
	if req.GetTopic() != "project-env-orders" {
		t.Errorf("topic = %q, want the bound name", req.GetTopic())
	}
	if string(req.GetPayload()) != `{"id":"o-1","total":30}` {
		t.Errorf("payload = %s", req.GetPayload())
	}
	if req.GetDueAt() != nil || req.GetLane() != topicv1.Lane_LANE_UNSPECIFIED || req.GetKey() != "" {
		t.Errorf("request = %v, want no option set", req)
	}
}

func TestSendCarriesItsDelayIdempotencyKeyKeyAndLane(t *testing.T) {
	runtime := serveRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_orders": boundTopic})
	orders := ocel.Topic[order]("orders")

	before := time.Now()
	if _, err := orders.Send(t.Context(), order{ID: "o-2"},
		ocel.Delay(time.Hour), ocel.IdempotencyKey("idem"), ocel.Key("customer-9"), ocel.LaneLow); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	req := only[*topicv1.SendRequest](t, runtime)
	due := req.GetDueAt().AsTime()
	if due.Before(before.Add(time.Hour)) || due.After(time.Now().Add(time.Hour)) {
		t.Errorf("dueAt = %v, want an hour from when Send was called", due)
	}
	if req.GetIdempotencyKey() != "idem" || req.GetKey() != "customer-9" || req.GetLane() != topicv1.Lane_LANE_LOW {
		t.Errorf("request = %v", req)
	}
}

func TestSendDueAtATimeCarriesThatTime(t *testing.T) {
	runtime := serveRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_orders": boundTopic})
	orders := ocel.Topic[order]("orders")
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

	if _, err := orders.Send(t.Context(), order{}, ocel.DelayUntil(at)); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if due := only[*topicv1.SendRequest](t, runtime).GetDueAt().AsTime(); !due.Equal(at) {
		t.Errorf("dueAt = %v, want %v", due, at)
	}
}

func TestSendRefusesAPayloadOver256KiBBeforeReachingTheRuntime(t *testing.T) {
	runtime := serveRuntime(t, map[string]string{"OCEL_RESOURCE_TOPIC_notes": `{"name":"notes","topic":{"topic":"project-env-notes"}}`})
	notes := ocel.Topic[string]("notes")

	_, err := notes.Send(t.Context(), strings.Repeat("a", 256<<10))

	if err == nil || !strings.Contains(err.Error(), "262144 bytes") {
		t.Errorf("Send() error = %v, want it to name the 262144-byte limit", err)
	}
	if received := runtime.requests(); len(received) != 0 {
		t.Errorf("the runtime received %v, want nothing", names(received))
	}
}

func TestTopicOperationsRefuseDuringDiscovery(t *testing.T) {
	discoveryDeclarations(t)
	orders := ocel.Topic[order]("orders-in-discovery")
	letters := orders.DeadLetter("audit")

	for access, err := range map[string]error{
		"Send":                        second(orders.Send(t.Context(), order{})),
		`DeadLetter("audit").List`:    second(letters.List(t.Context())),
		`DeadLetter("audit").Redrive`: second(letters.Redrive(t.Context())),
		`DeadLetter("audit").Purge`:   second(letters.Purge(t.Context())),
		`DeadLetter("audit").Count`:   second(letters.Count(t.Context())),
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
