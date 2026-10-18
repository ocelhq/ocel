package runs_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/pkg/runs/fake"
)

type pushed struct {
	engine *runs.Engine
	store  *fake.Store
	queue  *fake.Queue
	calls  *atomic.Int32
}

func aPushedEngine(t *testing.T, topics map[string]*provider.TopicSpec, status int, configure func(*runs.Config)) pushed {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	store, queue := fake.NewStore(), fake.NewQueue(topics)
	cfg := runs.Config{
		Store:      store,
		Queue:      queue,
		Deployment: runs.Deployment{Topics: topics, Workers: map[string]runs.Worker{"w": {URL: server.URL}}},
	}
	if configure != nil {
		configure(&cfg)
	}
	return pushed{engine: runs.New(cfg), store: store, queue: queue, calls: calls}
}

func aTaskNamed(name string, maxAttempts int, ordered bool) map[string]*provider.TopicSpec {
	return map[string]*provider.TopicSpec{name: {Ordered: ordered, Consumers: []provider.ConsumerSpec{{
		Name: name, Worker: "w", Exclusive: true,
		Retry: provider.RetryPolicy{MaxAttempts: maxAttempts, MinDelay: time.Second, MaxDelay: time.Second},
	}}}}
}

func (p pushed) received(t *testing.T, topic, consumer string) []runs.Received {
	t.Helper()
	received, err := p.queue.Receive(context.Background(), runs.ReceiveRequest{Topic: topic, Consumer: consumer, Max: 10, Hidden: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return received
}

func (p pushed) trigger(t *testing.T, task string, options *taskv1.TriggerOptions) string {
	t.Helper()
	resp, err := p.engine.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: task, Payload: []byte(`{}`), Options: options})
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetId()
}

func TestAMessageNamingARunThatNoLongerExistsIsDoneWith(t *testing.T) {
	t.Parallel()

	p := aPushedEngine(t, aTaskNamed("resize", 3, false), http.StatusOK, nil)
	gone := runs.Received{Handle: "1", Message: runs.Message{Execution: "01K0000000000000000000000A-resize", Delivery: "x"}}
	dispositions := p.engine.Deliver(context.Background(), "resize", "resize", []runs.Received{gone})
	if !dispositions[0].Done || p.calls.Load() != 0 {
		t.Errorf("Deliver = %+v after %d calls, want the message done with and the worker never called", dispositions[0], p.calls.Load())
	}
}

func TestAMessageCarryingADeliveryTheRunNoLongerHoldsIsDoneWithoutAnAttempt(t *testing.T) {
	t.Parallel()

	p := aPushedEngine(t, aTaskNamed("resize", 3, false), http.StatusOK, nil)
	id := p.trigger(t, "resize", nil)
	received := p.received(t, "resize", "resize")
	received[0].Message.Delivery = "an earlier delivery"
	dispositions := p.engine.Deliver(context.Background(), "resize", "resize", received)
	run, _ := p.store.ReadRun(context.Background(), "resize", id)
	if !dispositions[0].Done || p.calls.Load() != 0 || run.Status != provider.RunQueued {
		t.Errorf("Deliver = %+v, run %s after %d calls, want the stale message done with and the run untouched", dispositions[0], run.Status, p.calls.Load())
	}
}

func TestAMessageReadBeforeItsRunWasRescheduledLaterDoesNotRunIt(t *testing.T) {
	t.Parallel()

	p := aPushedEngine(t, aTaskNamed("remind", 3, false), http.StatusOK, nil)
	ctx := context.Background()
	id := p.trigger(t, "remind", &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Second))})
	read := []runs.Received{{Handle: "read", Message: p.queue.Sent()[0]}}
	if _, err := p.engine.Tasks().RescheduleRun(ctx, &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.New(time.Now().Add(time.Hour))}); err != nil {
		t.Fatalf("RescheduleRun: %v", err)
	}
	dispositions := p.engine.Deliver(ctx, "remind", "remind", read)
	run, _ := p.store.ReadRun(ctx, "remind", id)
	if !dispositions[0].Done || p.calls.Load() != 0 || run.Status != provider.RunDelayed || run.Attempts != 0 {
		t.Errorf("Deliver = %+v, run %s after %d attempts and %d calls, want the old message done with and the run still delayed and unattempted", dispositions[0], run.Status, run.Attempts, p.calls.Load())
	}
}

func TestAMessageOfARunLeasedElsewhereIsRedeliveredOnceTheLeaseRunsOut(t *testing.T) {
	t.Parallel()

	p := aPushedEngine(t, aTaskNamed("resize", 3, false), http.StatusOK, nil)
	id := p.trigger(t, "resize", nil)
	ctx := context.Background()
	run, _ := p.store.ReadRun(ctx, "resize", id)
	run.Status, run.Attempts, run.LeaseUntil = provider.RunExecuting, 1, time.Now().Add(time.Minute)
	if _, err := p.store.WriteRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	dispositions := p.engine.Deliver(ctx, "resize", "resize", p.received(t, "resize", "resize"))
	if dispositions[0].Done || dispositions[0].After < 50*time.Second || p.calls.Load() != 0 {
		t.Errorf("Deliver = %+v after %d calls, want the message redelivered once the minute's lease is out and no call", dispositions[0], p.calls.Load())
	}
}

func TestAFailedAttemptWithAttemptsLeftIsRedeliveredAfterItsBackoff(t *testing.T) {
	t.Parallel()

	p := aPushedEngine(t, aTaskNamed("resize", 3, false), http.StatusInternalServerError, nil)
	id := p.trigger(t, "resize", nil)
	dispositions := p.engine.Deliver(context.Background(), "resize", "resize", p.received(t, "resize", "resize"))
	run, _ := p.store.ReadRun(context.Background(), "resize", id)
	if dispositions[0].Done || dispositions[0].After < 500*time.Millisecond || run.Status != provider.RunQueued || run.Attempts != 1 {
		t.Errorf("Deliver = %+v, run %s after %d attempts, want it queued after 1 attempt and redelivered after its backoff", dispositions[0], run.Status, run.Attempts)
	}
}

func TestATopicMessageRecordsItsConsumersRunOnArrivalAndACopyFindsItSettled(t *testing.T) {
	t.Parallel()

	topics := map[string]*provider.TopicSpec{"orders": {Consumers: []provider.ConsumerSpec{{Name: "ship", Worker: "w", Retry: provider.RetryPolicy{MaxAttempts: 3}}}}}
	p := aPushedEngine(t, topics, http.StatusOK, nil)
	ctx := context.Background()
	sent, err := p.engine.Topics().Send(ctx, &topicv1.SendRequest{Topic: "orders", Payload: []byte(`{"n":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	copies := p.received(t, "orders", "ship")
	for range 2 {
		if dispositions := p.engine.Deliver(ctx, "orders", "ship", copies); !dispositions[0].Done {
			t.Fatalf("Deliver = %+v, want the message done with", dispositions[0])
		}
	}
	run, err := p.store.ReadRun(ctx, "orders", runs.ExecutionOf(sent.GetMessageId(), "ship"))
	if err != nil || run.Status != provider.RunCompleted || p.calls.Load() != 1 {
		t.Errorf("run %s (%v) after %d calls, want it completed by one call however often its message came", run.Status, err, p.calls.Load())
	}
}

func TestARunWhoseMessageCameTooOftenWithoutStartingFailsRatherThanBeDeadLetteredUnseen(t *testing.T) {
	t.Parallel()

	topics := map[string]*provider.TopicSpec{"report": {Consumers: []provider.ConsumerSpec{{Name: "report", Worker: "w", Exclusive: true, Concurrency: 1, Retry: provider.RetryPolicy{MaxAttempts: 3}}}}}
	p := aPushedEngine(t, topics, http.StatusOK, func(cfg *runs.Config) {
		cfg.MaxReceives = 5
		cfg.Slots = noSlots{}
	})
	id := p.trigger(t, "report", nil)
	received := p.received(t, "report", "report")
	received[0].Receives = 5
	dispositions := p.engine.Deliver(context.Background(), "report", "report", received)
	run, _ := p.store.ReadRun(context.Background(), "report", id)
	if !dispositions[0].Done || run.Status != provider.RunFailed || p.calls.Load() != 0 {
		t.Errorf("Deliver = %+v, run %s after %d calls, want the run failed and its message done with", dispositions[0], run.Status, p.calls.Load())
	}
}

type noSlots struct{}

func (noSlots) Take(context.Context, runs.SlotSet, string, time.Time) (runs.Slot, bool, error) {
	return runs.Slot{}, false, nil
}

func (noSlots) Renew(context.Context, runs.Slot, time.Time) error { return nil }

func (noSlots) Release(context.Context, runs.Slot) error { return nil }

func TestARunWithNoSlotFreeIsRedeliveredWithoutAnAttempt(t *testing.T) {
	t.Parallel()

	topics := map[string]*provider.TopicSpec{"report": {Consumers: []provider.ConsumerSpec{{Name: "report", Worker: "w", Exclusive: true, Concurrency: 1, Retry: provider.RetryPolicy{MaxAttempts: 3}}}}}
	p := aPushedEngine(t, topics, http.StatusOK, func(cfg *runs.Config) { cfg.Slots = noSlots{} })
	id := p.trigger(t, "report", nil)
	dispositions := p.engine.Deliver(context.Background(), "report", "report", p.received(t, "report", "report"))
	run, _ := p.store.ReadRun(context.Background(), "report", id)
	if dispositions[0].Done || dispositions[0].After < time.Second || run.Status != provider.RunQueued || p.calls.Load() != 0 {
		t.Errorf("Deliver = %+v, run %s after %d calls, want it redelivered after a slot's wait with the run untouched", dispositions[0], run.Status, p.calls.Load())
	}
}

func TestAMessageForAConsumerThisDeploymentDoesNotRunIsRedeliveredForOneThatDoes(t *testing.T) {
	t.Parallel()

	p := aPushedEngine(t, aTaskNamed("resize", 3, false), http.StatusOK, nil)
	dispositions := p.engine.Deliver(context.Background(), "thumbnail", "thumbnail", []runs.Received{{Handle: "1"}})
	if dispositions[0].Done {
		t.Errorf("Deliver = %+v, want the message redelivered", dispositions[0])
	}
}

func TestAnEarlyMessageOfAnOrderedTaskHoldsItsPlaceUntilARescheduleReleasesIt(t *testing.T) {
	t.Parallel()

	p := aPushedEngine(t, aTaskNamed("remind", 3, true), http.StatusOK, nil)
	ctx := context.Background()
	id := p.trigger(t, "remind", &taskv1.TriggerOptions{Key: "user-1", DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	early := runs.Received{Handle: "early", Message: p.queue.Sent()[0]}
	dispositions := p.engine.Deliver(ctx, "remind", "remind", []runs.Received{early})
	run, _ := p.store.ReadRun(ctx, "remind", id)
	if dispositions[0].Done || dispositions[0].After < 59*time.Minute || run.Held != "early" {
		t.Fatalf("Deliver = %+v holding %q, want the early message hidden for the hour and its handle held on the run", dispositions[0], run.Held)
	}
	released := &releases{}
	engine := runs.New(runs.Config{Store: p.store, Queue: released, Deployment: runs.Deployment{Topics: aTaskNamed("remind", 3, true), Workers: map[string]runs.Worker{"w": {}}}})
	if _, err := engine.Tasks().RescheduleRun(ctx, &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := released.all(); len(got) != 1 || got[0] != "early" {
		t.Errorf("the reschedule released %v, want the held message", got)
	}
}

type releases struct {
	mu       sync.Mutex
	released []string
}

func (r *releases) Send(context.Context, runs.Message) error { return nil }

func (r *releases) Release(_ context.Context, _, _, handle string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, handle)
	return nil
}

func (r *releases) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.released...)
}

func TestAKeysLaterMessagesWaitBehindOneRedeliveredWhileAnotherKeyRuns(t *testing.T) {
	t.Parallel()

	topics := map[string]*provider.TopicSpec{"ledger": {Ordered: true, Consumers: []provider.ConsumerSpec{{Name: "apply", Worker: "w", Retry: provider.RetryPolicy{MaxAttempts: 3, MinDelay: time.Second, MaxDelay: time.Second}}}}}
	p := aPushedEngine(t, topics, http.StatusInternalServerError, nil)
	ctx := context.Background()
	for _, key := range []string{"a", "a", "b"} {
		if _, err := p.engine.Topics().Send(ctx, &topicv1.SendRequest{Topic: "ledger", Key: key, Payload: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	dispositions := p.engine.Deliver(ctx, "ledger", "apply", p.received(t, "ledger", "apply"))
	if len(dispositions) != 3 || dispositions[0].Done || dispositions[1].Done || dispositions[1].After != 0 || p.calls.Load() != 2 {
		t.Errorf("Deliver = %+v after %d calls, want key a's first redelivered, its second waiting behind it untried, and key b tried", dispositions, p.calls.Load())
	}
}
