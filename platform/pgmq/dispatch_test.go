package pgmq

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ocelhq/ocel/pkg/envelope"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/topiclimits"
)

type reply struct {
	status int
	body   string
	hold   time.Duration
}

type delivered struct {
	envelope *topicv1.Envelope
	body     []byte
	header   http.Header
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
	posted, err := envelope.Decode(body)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	w.mu.Lock()
	w.envelopes = append(w.envelopes, delivered{envelope: posted, body: body, header: req.Header.Clone(), at: time.Now()})
	w.inFlight++
	w.peak = max(w.peak, w.inFlight)
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.inFlight--
		w.mu.Unlock()
	}()
	answer := w.respond(posted)
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

func fieldOf(raw []byte, name string) any {
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	return fields[name]
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
	if err := engine.Apply(context.Background(), Deployment{Slug: "app", Topics: topics, Workers: workers}); err != nil {
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
	if a := envelope.GetAttempt(); a.GetNumber() != 1 || a.GetOf() != topiclimits.DefaultRetryMaxAttempts || a.GetFirstAttemptedAt() == nil {
		t.Errorf("attempt = %v, want 1 of %d with when it was first attempted", a, topiclimits.DefaultRetryMaxAttempts)
	}
	if image := fieldOf(envelope.GetPayload(), "image"); image != "cat.png" {
		t.Errorf("payload = %s, want the triggered one", envelope.GetPayload())
	}
	if done := fieldOf(run.GetOutput(), "done"); done != true {
		t.Errorf("output = %s, want what the worker answered", run.GetOutput())
	}
	if run.GetAttempts() != 1 || run.GetTask() != "resize" || len(run.GetTags()) != 1 || run.GetStartedAt() == nil || run.GetFinishedAt() == nil {
		t.Errorf("run = %v, want one attempt of resize, tagged, started and finished", run)
	}
}

func TestAWorkersHeaderRidesOnEveryDeliveryToIt(t *testing.T) {
	worker := newWorker(t, succeeding)
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask()},
		map[string]Worker{"worker": {URL: worker.server.URL, Header: http.Header{"X-Ocel-Origin-Secret": {"only-the-engine-knows"}}}})

	awaitRun(t, engine, trigger(t, engine, "resize", `{}`, nil), taskv1.RunStatus_RUN_STATUS_COMPLETED)

	got := worker.received()
	if len(got) != 1 {
		t.Fatalf("the worker received %d deliveries, want 1", len(got))
	}
	if secret := got[0].header.Get("X-Ocel-Origin-Secret"); secret != "only-the-engine-knows" {
		t.Errorf("the delivery carried X-Ocel-Origin-Secret %q, want the header its deployment names", secret)
	}
}

func appliedWith(t *testing.T, cfg Config, topics map[string]*contractv1.ManifestTopic, workers map[string]Worker) *Engine {
	t.Helper()
	engine, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(engine.Close)
	if err := engine.Apply(context.Background(), Deployment{Slug: "app", Topics: topics, Workers: workers}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return engine
}

func dispatchingWithLease(t *testing.T, lease time.Duration, topics map[string]*contractv1.ManifestTopic, workers map[string]Worker) *Engine {
	t.Helper()
	cfg := aDatabase(t)
	cfg.Lease = lease
	engine := appliedWith(t, cfg, topics, workers)
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
	if err := engine.Apply(context.Background(), Deployment{Slug: "app", Topics: topics, Workers: served}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_COMPLETED)
}

func TestAnIdleQueuePollsHalfAsOftenAfterEachEmptyReadDownToOnceEveryTwoSeconds(t *testing.T) {
	t.Parallel()

	interval := pollInterval
	var got []time.Duration
	for range 6 {
		interval = lengthenPollInterval(interval)
		got = append(got, interval)
	}
	want := []time.Duration{400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 2 * time.Second, 2 * time.Second, 2 * time.Second}
	if !slices.Equal(got, want) {
		t.Errorf("poll intervals after each empty read = %v, want %v", got, want)
	}
}

func TestATriggerOnAQueueIdleLongEnoughToPollRarelyIsDeliveredAtOnce(t *testing.T) {
	engine, worker := aServedTask(t, succeeding)
	time.Sleep(4 * time.Second)

	triggered := time.Now()
	trigger(t, engine, "resize", `{}`, nil)
	if got := awaitDelivered(t, worker, 1); got[0].at.Sub(triggered) > time.Second {
		t.Errorf("the run reached the worker %v after its trigger, want the wake to cut the idle wait short", got[0].at.Sub(triggered))
	}
}

func stoppableDispatch(t *testing.T, engine *Engine) (stop func() time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Dispatch(ctx) }()
	var stopped bool
	stop = func() time.Duration {
		if stopped {
			return 0
		}
		stopped = true
		began := time.Now()
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Dispatch: %v", err)
		}
		return time.Since(began)
	}
	t.Cleanup(func() { stop() })
	return stop
}

func TestAnAttemptInFlightWhenTheEngineStopsIsLetFinishAndItsOutcomeRecorded(t *testing.T) {
	worker := newWorker(t, func(*topicv1.Envelope) reply {
		return reply{status: http.StatusOK, body: `"resized"`, hold: time.Second}
	})
	engine := applied(t, map[string]*contractv1.ManifestTopic{"resize": aTask()}, map[string]Worker{"worker": {URL: worker.server.URL}})
	stop := stoppableDispatch(t, engine)
	id := trigger(t, engine, "resize", `{}`, nil)
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_EXECUTING)

	stop()

	if run := retrieve(t, engine, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED {
		t.Errorf("run = %v after the engine stopped, want the attempt in flight let finish and completed", run.GetStatus())
	}
}

func TestStoppingTheEngineCutsAnAttemptStillRunningWhenTheShutdownGraceEnds(t *testing.T) {
	worker := newWorker(t, func(*topicv1.Envelope) reply { return reply{status: http.StatusOK, hold: 10 * time.Second} })
	cfg := aDatabase(t)
	cfg.ShutdownGrace = 300 * time.Millisecond
	engine := appliedWith(t, cfg, map[string]*contractv1.ManifestTopic{"resize": aTask(named("resize"))}, map[string]Worker{"worker": {URL: worker.server.URL}})
	stop := stoppableDispatch(t, engine)
	id := trigger(t, engine, "resize", `{}`, nil)
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_EXECUTING)

	if took := stop(); took < 300*time.Millisecond || took > 3*time.Second {
		t.Errorf("the engine stopped %v after it was asked to, want it to wait out its 300ms grace and no longer", took)
	}
	deadline := time.Now().Add(time.Second)
	for worker.inFlightNow() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if worker.inFlightNow() > 0 {
		t.Error("the worker still holds an attempt of an engine that stopped")
	}
}

func TestARunPastItsAttemptCapIsSettledFailedRatherThanRunAgain(t *testing.T) {
	worker := newWorker(t, func(*topicv1.Envelope) reply { return reply{status: http.StatusOK, hold: 5 * time.Second} })
	cfg := aDatabase(t)
	cfg.Lease, cfg.ShutdownGrace = time.Second, 100*time.Millisecond
	engine := appliedWith(t, cfg, map[string]*contractv1.ManifestTopic{"charge": aTask(named("charge"), retrying(1, time.Millisecond, time.Millisecond))}, map[string]Worker{"worker": {URL: worker.server.URL}})
	stop := stoppableDispatch(t, engine)
	id := trigger(t, engine, "charge", `{}`, nil)
	awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_EXECUTING)
	stop()

	stoppableDispatch(t, engine)
	run := awaitRun(t, engine, id, taskv1.RunStatus_RUN_STATUS_FAILED)

	if got := len(worker.received()); got != 1 || run.GetAttempts() != 1 {
		t.Errorf("the worker received %d attempts and the run counts %d, want the one attempt its cap of 1 allows", got, run.GetAttempts())
	}
	if run.GetError() == "" || run.GetFinishedAt() == nil {
		t.Errorf("run = %v, want it finished with why it failed", run)
	}
}
