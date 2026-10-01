package pgmq

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type reply struct {
	status int
	body   string
	hold   time.Duration
}

type delivered struct {
	envelope *topicv1.Envelope
	at       time.Time
}

type aWorker struct {
	server  *httptest.Server
	respond func(*topicv1.Envelope) reply

	mu        sync.Mutex
	envelopes []delivered
	inFlight  int
	peak      int
}

func newWorker(t *testing.T, respond func(*topicv1.Envelope) reply) *aWorker {
	t.Helper()
	w := &aWorker{respond: respond}
	w.server = httptest.NewServer(http.HandlerFunc(w.serve))
	t.Cleanup(w.server.Close)
	return w
}

func succeeding(*topicv1.Envelope) reply { return reply{status: http.StatusOK, body: `{"done":true}`} }

func (w *aWorker) serve(rw http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(req.Body)
	if err != nil || req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
		http.Error(rw, "want a JSON POST", http.StatusBadRequest)
		return
	}
	envelope := &topicv1.Envelope{}
	if err := protojson.Unmarshal(body, envelope); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	w.mu.Lock()
	w.envelopes = append(w.envelopes, delivered{envelope: envelope, at: time.Now()})
	w.inFlight++
	w.peak = max(w.peak, w.inFlight)
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.inFlight--
		w.mu.Unlock()
	}()
	answer := w.respond(envelope)
	if answer.hold > 0 {
		select {
		case <-time.After(answer.hold):
		case <-req.Context().Done():
			return
		}
	}
	rw.WriteHeader(answer.status)
	_, _ = io.WriteString(rw, answer.body)
}

func (w *aWorker) received() []delivered {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]delivered(nil), w.envelopes...)
}

func (w *aWorker) inFlightNow() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inFlight
}

func (w *aWorker) peakInFlight() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.peak
}

func aTask(mods ...func(*contractv1.ManifestTopic)) *contractv1.ManifestTopic {
	topic := &contractv1.ManifestTopic{Consumers: []*contractv1.ManifestConsumer{{Worker: "worker", Exclusive: true}}}
	for _, mod := range mods {
		mod(topic)
	}
	return topic
}

func retrying(maxAttempts int32, minDelay, maxDelay time.Duration) func(*contractv1.ManifestTopic) {
	return func(topic *contractv1.ManifestTopic) {
		topic.Retry = &resourcesv1.RetryPolicy{MaxAttempts: maxAttempts, MinDelay: durationpb.New(minDelay), MaxDelay: durationpb.New(maxDelay)}
	}
}

func applied(t *testing.T, topics map[string]*contractv1.ManifestTopic, workers map[string]Worker) *Engine {
	t.Helper()
	engine := anEngine(t)
	for name, topic := range topics {
		if topic.GetConsumers()[0].GetName() == "" && len(topic.GetConsumers()) == 1 {
			topic.Consumers[0].Name = name
		}
	}
	if err := engine.Apply(context.Background(), Deployment{Topics: topics, Workers: workers}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return engine
}

func dispatching(t *testing.T, topics map[string]*contractv1.ManifestTopic, workers map[string]Worker) *Engine {
	t.Helper()
	engine := applied(t, topics, workers)
	startDispatch(t, engine)
	return engine
}

func startDispatch(t *testing.T, engine *Engine) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Dispatch(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Dispatch: %v", err)
		}
	})
}

func trigger(t *testing.T, engine *Engine, task string, payload string, options *taskv1.TriggerOptions) string {
	t.Helper()
	resp, err := engine.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: task, Payload: []byte(payload), Options: options})
	if err != nil {
		t.Fatalf("Trigger(%s): %v", task, err)
	}
	return resp.GetId()
}

func awaitRun(t *testing.T, engine *Engine, id string, status taskv1.RunStatus) *taskv1.Run {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := engine.Tasks().RetrieveRun(context.Background(), &taskv1.RetrieveRunRequest{Id: id})
		if err != nil {
			t.Fatalf("RetrieveRun(%s): %v", id, err)
		}
		if resp.GetRun().GetStatus() == status {
			return resp.GetRun()
		}
		if time.Now().After(deadline) {
			t.Fatalf("run %s is %s, want %s", id, resp.GetRun().GetStatus(), status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestATriggeredRunIsPostedToItsWorkerAsAnEnvelopeAndCompletesWithTheWorkersAnswer(t *testing.T) {
	worker := newWorker(t, succeeding)
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask()}, map[string]Worker{"worker": {URL: worker.server.URL}})

	id := trigger(t, engine, "resize", `{"image":"cat.png"}`, &taskv1.TriggerOptions{Tags: []string{"eu"}})
	run := awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)

	got := worker.received()
	if len(got) != 1 {
		t.Fatalf("the worker received %d envelopes, want 1", len(got))
	}
	envelope := got[0].envelope
	if envelope.GetV() != 1 || envelope.GetTopic() != "resize" || envelope.GetConsumer() != "resize" || envelope.GetExecution() != id {
		t.Errorf("envelope = %v, want v1 for topic and consumer resize, execution %s", envelope, id)
	}
	if len(envelope.GetMessage().GetId()) != 26 || id != envelope.GetMessage().GetId()+"-resize" {
		t.Errorf("message id %q, want a ULID that the execution %s extends with the consumer", envelope.GetMessage().GetId(), id)
	}
	if envelope.GetMessage().GetPublishedAt() == nil {
		t.Error("the envelope has no publishedAt")
	}
	if a := envelope.GetAttempt(); a.GetNumber() != 1 || a.GetOf() != defaultMaxAttempts || a.GetFirstAttemptedAt() == nil {
		t.Errorf("attempt = %v, want 1 of %d with when it was first attempted", a, defaultMaxAttempts)
	}
	if image := envelope.GetPayload().GetStructValue().GetFields()["image"].GetStringValue(); image != "cat.png" {
		t.Errorf("payload = %v, want the triggered one", envelope.GetPayload())
	}
	if done := run.GetOutput().GetStructValue().GetFields()["done"].GetBoolValue(); !done {
		t.Errorf("output = %v, want what the worker answered", run.GetOutput())
	}
	if run.GetAttempts() != 1 || run.GetTask() != "resize" || len(run.GetTags()) != 1 || run.GetStartedAt() == nil || run.GetFinishedAt() == nil {
		t.Errorf("run = %v, want one attempt of resize, tagged, started and finished", run)
	}
}

func dispatchingWithLease(t *testing.T, lease time.Duration, topics map[string]*contractv1.ManifestTopic, workers map[string]Worker) *Engine {
	t.Helper()
	cfg := aDatabase(t)
	cfg.Lease = lease
	engine, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(engine.Close)
	if err := engine.Apply(context.Background(), Deployment{Topics: topics, Workers: workers}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	startDispatch(t, engine)
	return engine
}

func TestAnAttemptThatOutlivesItsLeaseIsNotDeliveredAgain(t *testing.T) {
	worker := newWorker(t, func(*topicv1.Envelope) reply { return reply{status: http.StatusOK, hold: 3 * time.Second} })
	engine := dispatchingWithLease(t, time.Second, map[string]*contractv1.ManifestTopic{"resize": aTask(named("resize"))}, map[string]Worker{"worker": {URL: worker.server.URL}})

	id := trigger(t, engine, "resize", `{}`, nil)
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)

	if got := len(worker.received()); got != 1 {
		t.Errorf("the worker received %d deliveries of a run whose attempt outlived its 1s lease, want 1", got)
	}
}

func TestAMessageInABatchBeingCollectedPastItsLeaseIsCollectedOnce(t *testing.T) {
	worker := newWorker(t, succeeding)
	engine := dispatchingWithLease(t, time.Second, map[string]*contractv1.ManifestTopic{"orders": aTopic(batching(10, 2500*time.Millisecond))}, map[string]Worker{"worker": {URL: worker.server.URL}})

	send(t, engine, "orders", `{"n":1}`, nil)
	got := awaitDelivered(t, worker, 1)
	time.Sleep(time.Second)

	if batches := batchesOf(worker.received()); len(batches) != 1 || len(got[0].envelope.GetMessages()) != 1 {
		t.Errorf("batches = %v, want the one message once in one batch", batches)
	}
}

func TestARetryKeepsItsBackoffWhenTheBackoffIsLongerThanTheLease(t *testing.T) {
	worker := newWorker(t, failingUntil(2))
	engine := dispatchingWithLease(t, time.Second, map[string]*contractv1.ManifestTopic{"resize": aTask(named("resize"), retrying(3, 4*time.Second, 4*time.Second))}, map[string]Worker{"worker": {URL: worker.server.URL}})

	id := trigger(t, engine, "resize", `{}`, nil)
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)

	got := worker.received()
	if len(got) != 2 {
		t.Fatalf("the worker received %d attempts, want 2", len(got))
	}
	if gap := got[1].at.Sub(got[0].at); gap < 2*time.Second {
		t.Errorf("the retry came %v after the failure, want at least half its 4s backoff, not the 1s lease", gap)
	}
}

func TestARunWaitingOnAnUnservedWorkerRunsOnceALaterDeploymentServesIt(t *testing.T) {
	worker := newWorker(t, succeeding)
	topics := map[string]*contractv1.ManifestTopic{"resize": aTask()}
	engine := dispatching(t, topics, map[string]Worker{"other": {URL: worker.server.URL}})
	id := trigger(t, engine, "resize", `{}`, nil)
	time.Sleep(500 * time.Millisecond)

	served := map[string]Worker{"other": {URL: worker.server.URL}, "worker": {URL: worker.server.URL}}
	if err := engine.Apply(context.Background(), Deployment{Topics: topics, Workers: served}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)
}
