//go:build integration

package topics_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/envelope"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

const exactJSON = `{"ratio":2.0,"id":9007199254740993,"count":2}`

type answer struct {
	status int
	body   string
	wait   time.Duration
}

type fakeWorker struct {
	mu        sync.Mutex
	envelopes []map[string]json.RawMessage
	answer    func(n int) answer
	url       string
}

func newFakeWorker(t *testing.T, answer func(n int) answer) *fakeWorker {
	t.Helper()
	worker := &fakeWorker{answer: answer}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		worker.mu.Lock()
		worker.envelopes = append(worker.envelopes, fields)
		reply := worker.answer(len(worker.envelopes))
		worker.mu.Unlock()
		if reply.wait > 0 {
			select {
			case <-time.After(reply.wait):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(reply.status)
		_, _ = io.WriteString(w, reply.body)
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

func always(status int, body string) func(int) answer {
	return func(int) answer { return answer{status: status, body: body} }
}

type delivering struct {
	t          *testing.T
	store      topics.Store
	deliveries topics.Deliveries
}

func newDelivering(t *testing.T, worker *fakeWorker) delivering {
	t.Helper()
	store := topics.Store{Clients: liveClients(t), Scope: scopeOf(t)}
	return delivering{t: t, store: store, deliveries: topics.Deliveries{Store: store, Topics: deployedTopics(), Worker: worker.url}}
}

func (d delivering) run(execution string) provider.Run {
	d.t.Helper()
	run, err := d.store.ReadRun(context.Background(), execution)
	if err != nil {
		d.t.Fatalf("ReadRun(%s) = %v", execution, err)
	}
	return run
}

func acked(code int) bool { return code >= 200 && code < 300 }

func TestLiveATaskRunReachesTheWorkerAsAnEnvelopeAndCompletesWithItsOutput(t *testing.T) {
	worker := newFakeWorker(t, always(http.StatusOK, exactJSON))
	d := newDelivering(t, worker)
	message := aMessage(exactJSON)

	if code := d.push("resize", "resize", message); !acked(code) {
		t.Fatalf("the push answered %d, want it acked", code)
	}

	received := worker.received()
	if len(received) != 1 {
		t.Fatalf("the worker got %d envelopes, want 1", len(received))
	}
	got := received[0]
	execution := message.messageID + "-resize"
	for field, want := range map[string]string{
		"topic": `"resize"`, "consumer": `"resize"`, "execution": `"` + execution + `"`, "payload": exactJSON, "v": "1",
		"schema": `"` + envelope.SchemaOf("{}") + `"`,
	} {
		if string(got[field]) != want {
			t.Errorf("the envelope's %s is %s, want %s", field, got[field], want)
		}
	}
	var attempt struct {
		Number int `json:"number"`
		Of     int `json:"of"`
	}
	_ = json.Unmarshal(got["attempt"], &attempt)
	if attempt.Number != 1 || attempt.Of != 3 {
		t.Errorf("the envelope's attempt is %s, want 1 of the task's 3", got["attempt"])
	}
	var message2 struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(got["message"], &message2)
	if message2.ID != message.messageID {
		t.Errorf("the envelope's message is %s, want id %s", got["message"], message.messageID)
	}

	run := d.run(execution)
	if run.Status != provider.RunCompleted || string(run.Output) != exactJSON || string(run.Payload) != exactJSON {
		t.Errorf("the run is %s with output %s and payload %s, want completed with %s byte for byte", run.Status, run.Output, run.Payload, exactJSON)
	}
	if run.Attempts != 1 || run.StartedAt.IsZero() || run.FinishedAt.IsZero() {
		t.Errorf("the run took %d attempts, started %v, finished %v, want one attempt with both times", run.Attempts, run.StartedAt, run.FinishedAt)
	}

	if code := d.push("resize", "resize", message); !acked(code) {
		t.Errorf("a redelivery of a completed run answered %d, want it acked", code)
	}
	if len(worker.received()) != 1 {
		t.Errorf("a redelivery of a completed run reached the worker again")
	}
}

func TestLiveAFailedAttemptIsRetriedUntilTheTasksAttemptsRunOut(t *testing.T) {
	worker := newFakeWorker(t, always(http.StatusInternalServerError, "not yet"))
	d := newDelivering(t, worker)
	message := aMessage(`{}`)
	execution := message.messageID + "-resize"

	for attempt := 1; attempt <= 2; attempt++ {
		if code := d.push("resize", "resize", message); acked(code) {
			t.Fatalf("attempt %d failed and its push answered %d, want it nacked so Pub/Sub retries with backoff", attempt, code)
		}
		if run := d.run(execution); run.Status != provider.RunQueued || run.Attempts != attempt || !strings.Contains(run.Error, "not yet") {
			t.Fatalf("after failed attempt %d the run is %s after %d attempts with error %q, want queued with the worker's answer", attempt, run.Status, run.Attempts, run.Error)
		}
	}
	if code := d.push("resize", "resize", message); !acked(code) {
		t.Fatalf("the last attempt answered %d, want it acked: no attempt is left", code)
	}
	run := d.run(execution)
	if run.Status != provider.RunFailed || run.Attempts != 3 || run.FinishedAt.IsZero() {
		t.Errorf("the run is %s after %d attempts, want failed after 3", run.Status, run.Attempts)
	}
}

func TestLiveATriggerThatLowersMaxAttemptsEndsTheRunSooner(t *testing.T) {
	d := newDelivering(t, newFakeWorker(t, always(http.StatusInternalServerError, "no")))
	message := aMessage(`{}`)
	message.attributes = map[string]string{topics.MaxAttemptsAttribute: "1"}

	if code := d.push("resize", "resize", message); !acked(code) {
		t.Fatalf("the only attempt answered %d, want it acked", code)
	}
	if run := d.run(message.messageID + "-resize"); run.Status != provider.RunFailed || run.Attempts != 1 {
		t.Errorf("the run is %s after %d attempts, want failed after the trigger's 1", run.Status, run.Attempts)
	}
}

func TestLiveAnAbortFailsTheRunWithoutARetry(t *testing.T) {
	abort, err := protojson.Marshal(&topicv1.Answer{Outcome: &topicv1.Answer_Abort{Abort: &topicv1.Abort{Reason: "no such image"}}})
	if err != nil {
		t.Fatal(err)
	}
	d := newDelivering(t, newFakeWorker(t, always(http.StatusUnprocessableEntity, string(abort))))
	message := aMessage(`{}`)

	if code := d.push("resize", "resize", message); !acked(code) {
		t.Fatalf("an aborted attempt answered %d, want it acked", code)
	}
	if run := d.run(message.messageID + "-resize"); run.Status != provider.RunFailed || run.Attempts != 1 || run.Error != "no such image" {
		t.Errorf("the run is %s after %d attempts with error %q, want failed after one with the abort's reason", run.Status, run.Attempts, run.Error)
	}
}

func TestLiveATaskPastItsMaxDurationTimesOutAndAConsumerPastItsIsRetried(t *testing.T) {
	d := newDelivering(t, newFakeWorker(t, func(int) answer { return answer{status: http.StatusOK, wait: 2 * time.Second} }))

	task := aMessage(`{}`)
	if code := d.push("slow", "slow", task); !acked(code) {
		t.Fatalf("a task past its maxDuration answered %d, want it acked", code)
	}
	if run := d.run(task.messageID + "-slow"); run.Status != provider.RunTimedOut {
		t.Errorf("the task's run is %s, want timed out with no retry", run.Status)
	}

	sent := aMessage(`{}`)
	if code := d.push("orders", "ship", sent); acked(code) {
		t.Fatalf("a consumer past its maxDuration answered %d, want it nacked so the attempt is retried", code)
	}
	if run := d.run(sent.messageID + "-ship"); run.Status != provider.RunQueued || run.Attempts != 1 {
		t.Errorf("the consumer's run is %s after %d attempts, want queued for a retry", run.Status, run.Attempts)
	}
}

func TestLiveAConsumersLastFailureKeepsThePayloadToRedrive(t *testing.T) {
	d := newDelivering(t, newFakeWorker(t, always(http.StatusInternalServerError, "no")))
	sent := aMessage(exactJSON)
	execution := sent.messageID + "-ship"

	if code := d.push("orders", "ship", sent); acked(code) {
		t.Fatalf("the first of 2 attempts answered %d, want it nacked", code)
	}
	if run := d.run(execution); len(run.Payload) != 0 {
		t.Errorf("a consumer's queued run holds payload %s, want none: the message carries it", run.Payload)
	}
	if code := d.push("orders", "ship", sent); !acked(code) {
		t.Fatalf("the last attempt answered %d, want it acked", code)
	}
	if run := d.run(execution); run.Status != provider.RunFailed || string(run.Payload) != exactJSON {
		t.Errorf("the dead-lettered run is %s with payload %s, want failed holding %s to redrive", run.Status, run.Payload, exactJSON)
	}
}

func TestLiveACanceledOrExpiredRunIsAckedWithoutReachingTheWorker(t *testing.T) {
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	d := newDelivering(t, worker)
	ctx := context.Background()

	canceled := aMessage(`{}`)
	if _, err := d.store.WriteRun(ctx, provider.Run{Execution: canceled.messageID + "-resize", Topic: "resize", Consumer: "resize", Status: provider.RunCanceled, CreatedAt: canceled.publishedAt}); err != nil {
		t.Fatal(err)
	}
	expired := aMessage(`{}`)
	if _, err := d.store.WriteRun(ctx, provider.Run{Execution: expired.messageID + "-resize", Topic: "resize", Consumer: "resize", Status: provider.RunQueued, CreatedAt: expired.publishedAt, ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}

	for name, message := range map[string]push{"canceled": canceled, "expired": expired} {
		if code := d.push("resize", "resize", message); !acked(code) {
			t.Errorf("the %s run's push answered %d, want it acked", name, code)
		}
	}
	if len(worker.received()) != 0 {
		t.Errorf("the worker got %d envelopes, want none", len(worker.received()))
	}
	if run := d.run(expired.messageID + "-resize"); run.Status != provider.RunExpired || run.FinishedAt.IsZero() {
		t.Errorf("the run past its ttl is %s, want expired", run.Status)
	}
	if run := d.run(canceled.messageID + "-resize"); run.Status != provider.RunCanceled {
		t.Errorf("the canceled run is %s, want it left canceled", run.Status)
	}
}

func TestLiveCancelingAnExecutingRunAbortsItsAttemptAndAcksItWithoutARetry(t *testing.T) {
	worker := newFakeWorker(t, func(int) answer { return answer{status: http.StatusOK, wait: time.Minute} })
	d := newDelivering(t, worker)
	message := aMessage(`{}`)
	execution := message.messageID + "-resize"

	pushed := make(chan int, 1)
	go func() { pushed <- d.push("resize", "resize", message) }()
	deadline := time.Now().Add(10 * time.Second)
	for len(worker.received()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the attempt never reached the worker")
		}
		time.Sleep(20 * time.Millisecond)
	}
	run := d.run(execution)
	run.Status, run.FinishedAt = provider.RunCanceled, time.Now()
	if _, err := d.store.WriteRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-pushed:
		if !acked(code) {
			t.Errorf("the canceled attempt answered %d, want it acked so it is not retried", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the attempt still runs 10s after its run was canceled, want it aborted")
	}
	if run := d.run(execution); run.Status != provider.RunCanceled || run.Attempts != 1 {
		t.Errorf("the run is %s after %d attempts, want left canceled after one", run.Status, run.Attempts)
	}
}

func TestLiveAMessageWithNoOcelIDTakesOneDrawnFromItsPubSubID(t *testing.T) {
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	d := newDelivering(t, worker)
	at := time.Now().UTC().Truncate(time.Millisecond)

	if code := d.push("resize", "resize", push{publishedAt: at, payload: `{}`}); !acked(code) {
		t.Fatalf("a scheduled message answered %d, want it acked", code)
	}
	if code := d.push("resize", "resize", push{publishedAt: at, payload: `{}`}); !acked(code) {
		t.Fatalf("its redelivery answered %d, want it acked", code)
	}
	if received := worker.received(); len(received) != 1 {
		t.Fatalf("the worker got %d envelopes for one scheduled message, want 1", len(received))
	}
	execution := envelope.MessageIDFrom(at, "17") + "-resize"
	if run := d.run(execution); run.Status != provider.RunCompleted {
		t.Errorf("the scheduled run %s is %s, want completed", execution, run.Status)
	}
}

func TestLiveAMessageWithNoOcelIDOrPublishTimeKeepsOneRunAcrossRedeliveries(t *testing.T) {
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	d := newDelivering(t, worker)

	for range 2 {
		if code := d.push("resize", "resize", push{payload: `{}`}); !acked(code) {
			t.Fatalf("the push answered %d, want it acked", code)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if received := worker.received(); len(received) != 1 {
		t.Errorf("the worker got %d envelopes for one message delivered twice, want 1", len(received))
	}
	execution := envelope.MessageIDFrom(time.Unix(0, 0), "17") + "-resize"
	if run := d.run(execution); run.Status != provider.RunCompleted {
		t.Errorf("the run %s is %s, want completed", execution, run.Status)
	}
}

func TestLiveABatchConsumerGetsEachMessageAsABatchOfOne(t *testing.T) {
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	d := newDelivering(t, worker)
	sent := aMessage(exactJSON)

	if code := d.push("orders", "digest", sent); !acked(code) {
		t.Fatalf("the push answered %d, want it acked", code)
	}
	received := worker.received()
	if len(received) != 1 {
		t.Fatalf("the worker got %d envelopes, want 1", len(received))
	}
	var deliveries []map[string]json.RawMessage
	_ = json.Unmarshal(received[0]["messages"], &deliveries)
	if len(deliveries) != 1 || string(deliveries[0]["payload"]) != exactJSON {
		t.Errorf("the batch envelope carries %s, want one delivery of %s", received[0]["messages"], exactJSON)
	}
}

func TestLiveADelayedSendIsAgedFromItsDueTimeNotItsSendTime(t *testing.T) {
	worker := newFakeWorker(t, always(http.StatusOK, `{}`))
	d := newDelivering(t, worker)
	sent := aMessage(`{}`)
	sent.publishedAt = sent.publishedAt.Add(-48 * time.Hour)
	due := time.Now().UTC().Truncate(time.Millisecond)
	sent.attributes = map[string]string{topics.DueAtAttribute: due.Format(time.RFC3339Nano)}

	if code := d.push("orders", "ship", sent); !acked(code) {
		t.Fatalf("the push answered %d, want it acked", code)
	}
	if len(worker.received()) != 1 {
		t.Fatal("a send due now with a day of ttl left never reached the worker")
	}
	run := d.run(sent.messageID + "-ship")
	if run.Status != provider.RunCompleted || !run.DueAt.Equal(due) || !run.ExpiresAt.Equal(due.Add(24*time.Hour)) {
		t.Errorf("the run is %s due %v expiring %v, want completed, due %v and expiring a ttl after it", run.Status, run.DueAt, run.ExpiresAt, due)
	}
}

func (d delivering) push(topic, consumer string, p push) int {
	d.t.Helper()
	req := httptest.NewRequest(http.MethodPost, topics.PushPath(topic, consumer), bytes.NewReader(pushBody(p)))
	recorder := httptest.NewRecorder()
	d.deliveries.ServeHTTP(recorder, req)
	return recorder.Code
}
