package topics_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/pkg/runs/fake"
	"github.com/ocelhq/ocel/pkg/topiclimits"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const exactJSON = `{"ratio":2.0,"id":9007199254740993,"count":2}`

var defaultRetry = provider.RetryPolicy{MaxAttempts: topiclimits.DefaultRetryMaxAttempts, MinDelay: topiclimits.DefaultRetryMinDelay, MaxDelay: topiclimits.DefaultRetryMaxDelay}

func deployedTopics() map[string]*provider.TopicSpec {
	twice := provider.RetryPolicy{MaxAttempts: 2, MinDelay: time.Millisecond, MaxDelay: time.Millisecond}
	return map[string]*provider.TopicSpec{
		"resize": {
			Schema:    "{}",
			Consumers: []provider.ConsumerSpec{{Name: "resize", Worker: "worker", Exclusive: true, Retry: defaultRetry}},
		},
		"orders": {
			Ordered:   true,
			Consumers: []provider.ConsumerSpec{{Name: "ship", Worker: "worker", Retry: twice}},
		},
	}
}

type answer struct {
	status int
	body   string
}

type fakeWorker struct {
	mu        sync.Mutex
	envelopes []map[string]json.RawMessage
	answer    answer
	url       string
}

func newFakeWorker(t *testing.T, reply answer) *fakeWorker {
	t.Helper()
	worker := &fakeWorker{answer: reply}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		worker.mu.Lock()
		worker.envelopes = append(worker.envelopes, fields)
		worker.mu.Unlock()
		w.WriteHeader(worker.answer.status)
		_, _ = io.WriteString(w, worker.answer.body)
	}))
	t.Cleanup(server.Close)
	worker.url = server.URL
	return worker
}

func (w *fakeWorker) received() []map[string]json.RawMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]map[string]json.RawMessage(nil), w.envelopes...)
}

type delivering struct {
	t          *testing.T
	store      *fake.Store
	deliveries topics.Deliveries
}

func newDelivering(t *testing.T, worker *fakeWorker) delivering {
	t.Helper()
	declared := deployedTopics()
	store := fake.NewStore()
	engine := runs.New(runs.Config{
		Store:      store,
		Queue:      fake.NewQueue(declared),
		Deployment: runs.Deployment{Topics: declared, Workers: map[string]runs.Worker{"worker": {URL: worker.url}}},
	})
	return delivering{t: t, store: store, deliveries: topics.Deliveries{Engine: engine}}
}

func (d delivering) push(path string, body []byte) int {
	d.t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	d.deliveries.ServeHTTP(recorder, req)
	return recorder.Code
}

func (d delivering) run(topic, execution string) runs.Run {
	d.t.Helper()
	run, err := d.store.ReadRun(context.Background(), topic, execution)
	if err != nil {
		d.t.Fatalf("ReadRun(%s) = %v", execution, err)
	}
	return run
}

type push struct {
	pubsubID    string
	publishTime time.Time
	payload     string
	attributes  map[string]string
}

func (p push) body() []byte {
	message := map[string]any{
		"data":       base64.StdEncoding.EncodeToString([]byte(p.payload)),
		"attributes": p.attributes,
		"messageId":  p.pubsubID,
	}
	if !p.publishTime.IsZero() {
		message["publishTime"] = p.publishTime.UTC().Format(time.RFC3339Nano)
	}
	body, _ := json.Marshal(map[string]any{"message": message, "subscription": "projects/floci-local/subscriptions/whatever"})
	return body
}

func aMessage(payload string) (push, string) {
	at := time.Now().UTC().Truncate(time.Millisecond)
	id := runs.NewMessageID(at)
	return push{pubsubID: "17", publishTime: at, payload: payload, attributes: map[string]string{
		topics.MessageAttribute:     id,
		topics.PublishedAtAttribute: at.Format(time.RFC3339Nano),
	}}, id
}

func TestAPushToAPathNamingNoConsumerIsRefused(t *testing.T) {
	d := newDelivering(t, newFakeWorker(t, answer{status: http.StatusOK}))
	message, _ := aMessage(`{}`)
	for _, path := range []string{"/elsewhere", "/topics/orders/consumers/", "/topics/orders/consumers/ship/more", "/topics//consumers/ship"} {
		if code := d.push(path, message.body()); code != http.StatusNotFound {
			t.Errorf("a push to %s answered %d, want %d", path, code, http.StatusNotFound)
		}
	}
}

func TestAPushThatIsNotAPubSubMessageIsRefused(t *testing.T) {
	d := newDelivering(t, newFakeWorker(t, answer{status: http.StatusOK}))
	for name, body := range map[string]string{
		"not JSON":         "{",
		"data not base64":  `{"message":{"data":"!!","messageId":"17"}}`,
		"message not JSON": `{"message":"text"}`,
	} {
		if code := d.push(topics.PushPath("orders", "ship"), []byte(body)); code != http.StatusBadRequest {
			t.Errorf("a push whose %s answered %d, want %d", name, code, http.StatusBadRequest)
		}
	}
}

func TestAPushWhoseRunCompletesIsAcknowledgedAndHandsTheWorkerItsPayloadByteForByte(t *testing.T) {
	worker := newFakeWorker(t, answer{status: http.StatusOK, body: exactJSON})
	d := newDelivering(t, worker)
	message, id := aMessage(exactJSON)

	if code := d.push(topics.PushPath("resize", "resize"), message.body()); code != http.StatusNoContent {
		t.Fatalf("the push answered %d, want %d", code, http.StatusNoContent)
	}

	received := worker.received()
	if len(received) != 1 || string(received[0]["payload"]) != exactJSON || string(received[0]["execution"]) != `"`+runs.ExecutionOf(id, "resize")+`"` {
		t.Fatalf("the worker got %v, want one envelope of execution %s carrying %s", received, runs.ExecutionOf(id, "resize"), exactJSON)
	}
	run := d.run("resize", runs.ExecutionOf(id, "resize"))
	if run.Status != provider.RunCompleted || string(run.Output) != exactJSON || string(run.Payload) != exactJSON {
		t.Errorf("the run is %s with output %s and payload %s, want completed with %s byte for byte", run.Status, run.Output, run.Payload, exactJSON)
	}
}

func TestAPushWhoseAttemptFailsWithAttemptsLeftIsRedeliveredAndItsLastFailureAcknowledged(t *testing.T) {
	d := newDelivering(t, newFakeWorker(t, answer{status: http.StatusInternalServerError, body: "boom"}))
	message, id := aMessage(`{"n":1}`)
	execution := runs.ExecutionOf(id, "ship")

	if code := d.push(topics.PushPath("orders", "ship"), message.body()); code != http.StatusServiceUnavailable {
		t.Fatalf("the first failed attempt answered %d, want %d so Pub/Sub redelivers it", code, http.StatusServiceUnavailable)
	}
	if run := d.run("orders", execution); run.Status != provider.RunQueued || run.Attempts != 1 {
		t.Fatalf("after one failure the run is %s with %d attempts, want queued with 1", run.Status, run.Attempts)
	}
	if code := d.push(topics.PushPath("orders", "ship"), message.body()); code != http.StatusNoContent {
		t.Fatalf("the last failed attempt answered %d, want %d", code, http.StatusNoContent)
	}
	if run := d.run("orders", execution); run.Status != provider.RunFailed || run.Attempts != 2 || string(run.Payload) != `{"n":1}` {
		t.Errorf("after its last failure the run is %s with %d attempts and payload %s, want failed after 2 keeping the payload to redrive", run.Status, run.Attempts, run.Payload)
	}
}

func TestAPushForAConsumerThisDeploymentDoesNotRunIsRedelivered(t *testing.T) {
	worker := newFakeWorker(t, answer{status: http.StatusOK})
	d := newDelivering(t, worker)
	message, _ := aMessage(`{}`)

	if code := d.push(topics.PushPath("orders", "nobody"), message.body()); code != http.StatusServiceUnavailable {
		t.Errorf("a push for a consumer no one deployed answered %d, want %d", code, http.StatusServiceUnavailable)
	}
	if len(worker.received()) != 0 {
		t.Error("the worker was handed a message for a consumer it does not run")
	}
}

func TestAScheduledMessageRunsItsTaskWithTheMinuteItFiredAt(t *testing.T) {
	worker := newFakeWorker(t, answer{status: http.StatusOK, body: `{}`})
	d := newDelivering(t, worker)
	fired := time.Date(2026, 10, 2, 9, 5, 0, 412_000_000, time.UTC)
	scheduled := push{pubsubID: "17", publishTime: fired, payload: "{}", attributes: map[string]string{topics.ScheduleAttribute: "true"}}

	if code := d.push(topics.PushPath("resize", "resize"), scheduled.body()); code != http.StatusNoContent {
		t.Fatalf("the scheduled push answered %d, want %d", code, http.StatusNoContent)
	}
	received := worker.received()
	if len(received) != 1 || string(received[0]["payload"]) != string(runs.CronPayload(fired)) {
		t.Fatalf("the worker got %v, want one envelope carrying %s", received, runs.CronPayload(fired))
	}
	if run := d.run("resize", runs.ExecutionOf(runs.MessageIDFrom(fired, "17"), "resize")); run.Status != provider.RunCompleted {
		t.Errorf("the scheduled run is %s, want completed", run.Status)
	}
}

func TestAMessageWithNoOcelIDOrPublishTimeKeepsOneRunAcrossRedeliveries(t *testing.T) {
	worker := newFakeWorker(t, answer{status: http.StatusOK, body: `{}`})
	d := newDelivering(t, worker)
	bare := push{pubsubID: "42", payload: `{}`}

	for range 2 {
		if code := d.push(topics.PushPath("resize", "resize"), bare.body()); code != http.StatusNoContent {
			t.Fatalf("the push answered %d, want %d", code, http.StatusNoContent)
		}
	}
	if got := len(worker.received()); got != 1 {
		t.Errorf("the worker was handed %d envelopes, want 1: a redelivery names the run the first delivery made", got)
	}
	if run := d.run("resize", runs.ExecutionOf(runs.MessageIDFrom(time.Unix(0, 0), "42"), "resize")); run.Status != provider.RunCompleted {
		t.Errorf("the run is %s, want completed", run.Status)
	}
}

func TestAMessageCarryingADeliveryTokenItsRunNoLongerHoldsIsAcknowledgedWithoutRunning(t *testing.T) {
	worker := newFakeWorker(t, answer{status: http.StatusOK, body: `{}`})
	d := newDelivering(t, worker)
	now := time.Now().UTC().Truncate(time.Millisecond)
	id := runs.NewMessageID(now)
	execution := runs.ExecutionOf(id, "resize")
	recorded := runs.Run{
		Execution: execution, Topic: "resize", Consumer: "resize", Status: provider.RunQueued, Payload: []byte(`{}`),
		MaxAttempts: 3, Message: id, PublishedAt: now, Delivery: "rescheduled", CreatedAt: now, DueAt: now,
	}
	if _, err := d.store.CreateRun(context.Background(), recorded); err != nil {
		t.Fatal(err)
	}
	stale := push{pubsubID: "17", publishTime: now, payload: `{}`, attributes: map[string]string{
		topics.MessageAttribute: id, topics.ExecutionAttribute: execution, topics.DeliveryAttribute: "superseded",
	}}

	if code := d.push(topics.PushPath("resize", "resize"), stale.body()); code != http.StatusNoContent {
		t.Fatalf("the superseded message answered %d, want %d", code, http.StatusNoContent)
	}
	if len(worker.received()) != 0 || d.run("resize", execution).Status != provider.RunQueued {
		t.Errorf("the superseded message reached the worker or changed its run")
	}
}

func TestAMessageNamingARunThatIsGoneIsAcknowledged(t *testing.T) {
	worker := newFakeWorker(t, answer{status: http.StatusOK})
	d := newDelivering(t, worker)
	orphan := push{pubsubID: "17", publishTime: time.Now(), payload: `{}`, attributes: map[string]string{
		topics.MessageAttribute: "01K0000000000000000000000A", topics.ExecutionAttribute: "01K0000000000000000000000A-resize", topics.DeliveryAttribute: "d",
	}}

	if code := d.push(topics.PushPath("resize", "resize"), orphan.body()); code != http.StatusNoContent {
		t.Errorf("a message naming a purged run answered %d, want %d", code, http.StatusNoContent)
	}
	if len(worker.received()) != 0 {
		t.Error("a message naming a purged run reached the worker")
	}
}
