package runsconformance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	workerName = "w"
	patience   = 30 * time.Second
	lease      = 3 * time.Second
)

type Wiring struct {
	Store    runs.Store
	Queue    runs.Queue
	Receiver runs.Receiver
}

type harness struct {
	t       *testing.T
	wiring  Wiring
	records *gatedRecords
	topics  map[string]*provider.TopicSpec
	worker  *worker

	mu       sync.Mutex
	attempts []runs.Attempt
}

func retrying(maxAttempts int) provider.RetryPolicy {
	return provider.RetryPolicy{MaxAttempts: maxAttempts, MinDelay: 10 * time.Millisecond, MaxDelay: 20 * time.Millisecond}
}

func aTask(name string, consumer provider.ConsumerSpec) map[string]*provider.TopicSpec {
	consumer.Name, consumer.Worker, consumer.Exclusive = name, workerName, true
	return map[string]*provider.TopicSpec{name: {Consumers: []provider.ConsumerSpec{consumer}}}
}

func aTopic(name string, ordered bool, consumers ...provider.ConsumerSpec) map[string]*provider.TopicSpec {
	for i := range consumers {
		consumers[i].Worker = workerName
	}
	return map[string]*provider.TopicSpec{name: {Ordered: ordered, Consumers: consumers}}
}

func newHarness(t *testing.T, open func(t *testing.T, topics map[string]*provider.TopicSpec) Wiring, topics map[string]*provider.TopicSpec, answering func(*http.Request, *topicv1.Envelope, int) answer) *harness {
	t.Helper()
	wiring := open(t, topics)
	return &harness{t: t, wiring: wiring, records: &gatedRecords{Store: wiring.Store}, topics: topics, worker: newWorker(t, answering)}
}

type gatedRecords struct {
	runs.Store

	mu      sync.Mutex
	waiting int
	open    chan struct{}
}

func (g *gatedRecords) holdEnsuredUntil(callers int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.waiting, g.open = callers, make(chan struct{})
}

func (g *gatedRecords) EnsureRecord(ctx context.Context, record provider.ExpiringRecord) (provider.ExpiringRecord, bool, error) {
	held, created, err := g.Store.EnsureRecord(ctx, record)
	g.mu.Lock()
	open := g.open
	if g.waiting > 0 {
		g.waiting--
		if g.waiting == 0 {
			close(g.open)
			g.open = nil
		}
	} else {
		open = nil
	}
	g.mu.Unlock()
	if open != nil {
		select {
		case <-open:
		case <-ctx.Done():
		}
	}
	return held, created, err
}

func (h *harness) engine(grace time.Duration) *runs.Engine {
	return h.engineLeasing(grace, lease)
}

func (h *harness) engineLeasing(grace, leased time.Duration) *runs.Engine {
	return runs.New(runs.Config{
		Store:         h.records,
		Queue:         h.wiring.Queue,
		Deployment:    runs.Deployment{Topics: h.topics, Workers: map[string]runs.Worker{workerName: {URL: h.worker.url}}},
		Lease:         leased,
		ShutdownGrace: grace,
		ReportAttempt: func(attempt runs.Attempt) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.attempts = append(h.attempts, attempt)
		},
	})
}

func (h *harness) reported() []runs.Attempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.attempts)
}

func (h *harness) serve(engine *runs.Engine, topic string) (stop func()) {
	ctx, cancel := context.WithCancel(context.Background())
	var serving sync.WaitGroup
	for _, consumer := range h.topics[topic].Consumers {
		serving.Go(func() { engine.Serve(ctx, h.wiring.Receiver, topic, consumer.Name) })
	}
	stopped := false
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		serving.Wait()
	}
	h.t.Cleanup(stop)
	return stop
}

func (h *harness) trigger(engine *runs.Engine, task string, payload string, options *taskv1.TriggerOptions) string {
	h.t.Helper()
	resp, err := engine.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: task, Payload: []byte(payload), Options: options})
	if err != nil {
		h.t.Fatalf("Trigger %s: %v", task, err)
	}
	return resp.GetId()
}

func (h *harness) send(engine *runs.Engine, topic, key, payload string) string {
	h.t.Helper()
	resp, err := engine.Topics().Send(context.Background(), &topicv1.SendRequest{Topic: topic, Key: key, Payload: []byte(payload)})
	if err != nil {
		h.t.Fatalf("Send to %s: %v", topic, err)
	}
	return resp.GetMessageId()
}

func (h *harness) run(topic, execution string) runs.Run {
	h.t.Helper()
	run, err := h.wiring.Store.ReadRun(context.Background(), topic, execution)
	if err != nil {
		h.t.Fatalf("ReadRun %s: %v", execution, err)
	}
	return run
}

func (h *harness) eventually(what string, holds func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(patience)
	for !holds() {
		if time.Now().After(deadline) {
			h.t.Fatalf("waited %v for %s", patience, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) settlesAs(topic, execution string, status provider.RunStatus) runs.Run {
	h.t.Helper()
	var run runs.Run
	h.eventually(fmt.Sprintf("run %s to be %s", execution, status), func() bool {
		read, err := h.wiring.Store.ReadRun(context.Background(), topic, execution)
		run = read
		return err == nil && read.Status == status
	})
	return run
}

func (h *harness) stillAfter(wait time.Duration, what string, holds func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if !holds() {
			h.t.Fatalf("%s stopped holding", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func blockUntilCanceled(r *http.Request, _ *topicv1.Envelope, _ int) answer {
	<-r.Context().Done()
	return answer{status: http.StatusServiceUnavailable}
}

func payloadN(delivered *topicv1.Envelope) int {
	var payload struct {
		N int `json:"n"`
	}
	_ = json.Unmarshal(delivered.GetPayload(), &payload)
	return payload.N
}

func Engine(t *testing.T, open func(t *testing.T, topics map[string]*provider.TopicSpec) Wiring) {
	t.Helper()

	t.Run("a task's run completes on the worker's first answer", func(t *testing.T) {
		h := newHarness(t, open, aTask("resize", provider.ConsumerSpec{Retry: retrying(3)}), answerAlways(answeredOK))
		engine := h.engine(time.Second)
		h.serve(engine, "resize")
		id := h.trigger(engine, "resize", `{"n":1}`, nil)
		run := h.settlesAs("resize", id, provider.RunCompleted)
		if run.Attempts != 1 || string(run.Output) != `{"ok":true}` || h.worker.calls() != 1 {
			t.Errorf("run took %d attempts with output %s over %d calls, want 1 attempt answering {\"ok\":true} in 1 call", run.Attempts, run.Output, h.worker.calls())
		}
	})

	t.Run("a run whose worker keeps failing is attempted exactly its cap and then fails", func(t *testing.T) {
		h := newHarness(t, open, aTask("charge", provider.ConsumerSpec{Retry: retrying(3)}), answerAlways(answer{status: http.StatusInternalServerError, body: "boom"}))
		engine := h.engine(time.Second)
		h.serve(engine, "charge")
		id := h.trigger(engine, "charge", `{}`, nil)
		run := h.settlesAs("charge", id, provider.RunFailed)
		h.stillAfter(time.Second, "three calls to the worker", func() bool { return h.worker.calls() == 3 })
		if run.Attempts != 3 {
			t.Errorf("run took %d attempts, want 3", run.Attempts)
		}
	})

	t.Run("a run found past the lease of its final attempt fails without running again", func(t *testing.T) {
		h := newHarness(t, open, aTask("charge", provider.ConsumerSpec{Retry: retrying(1)}), answerAlways(answeredOK))
		engine := h.engine(time.Second)
		id := h.trigger(engine, "charge", `{}`, nil)
		crashed := h.run("charge", id)
		crashed.Status, crashed.Attempts, crashed.StartedAt, crashed.LeaseUntil = provider.RunExecuting, 1, time.Now().Add(-time.Minute), time.Now().Add(-time.Second)
		if _, err := h.wiring.Store.WriteRun(context.Background(), crashed); err != nil {
			t.Fatal(err)
		}
		h.serve(engine, "charge")
		run := h.settlesAs("charge", id, provider.RunFailed)
		if run.Attempts != 1 || h.worker.calls() != 0 {
			t.Errorf("run took %d attempts over %d calls, want its one attempt and no call", run.Attempts, h.worker.calls())
		}
	})

	t.Run("an attempt cut off by a stopping engine runs again on the next while attempts are left", func(t *testing.T) {
		h := newHarness(t, open, aTask("export", provider.ConsumerSpec{Retry: retrying(2)}), blockUntilCanceled)
		first := h.engine(50 * time.Millisecond)
		stop := h.serve(first, "export")
		id := h.trigger(first, "export", `{}`, nil)
		h.eventually("the first attempt to start", func() bool { return h.worker.calls() == 1 })
		stop()
		released := h.settlesAs("export", id, provider.RunQueued)
		if released.Attempts != 1 {
			t.Fatalf("the cut-off run holds %d attempts, want the 1 it used", released.Attempts)
		}
		h.worker.answerWith(answerAlways(answeredOK))
		h.serve(h.engine(time.Second), "export")
		run := h.settlesAs("export", id, provider.RunCompleted)
		if run.Attempts != 2 || h.worker.calls() != 2 {
			t.Errorf("run took %d attempts over %d calls, want 2 of each", run.Attempts, h.worker.calls())
		}
	})

	t.Run("an attempt cut off by a stopping engine on its final attempt fails the run", func(t *testing.T) {
		h := newHarness(t, open, aTask("charge", provider.ConsumerSpec{Retry: retrying(1)}), blockUntilCanceled)
		first := h.engine(50 * time.Millisecond)
		stop := h.serve(first, "charge")
		id := h.trigger(first, "charge", `{}`, nil)
		h.eventually("the attempt to start", func() bool { return h.worker.calls() == 1 })
		stop()
		h.settlesAs("charge", id, provider.RunFailed)
		h.worker.answerWith(answerAlways(answeredOK))
		h.serve(h.engine(time.Second), "charge")
		h.stillAfter(time.Second, "one call to the worker", func() bool { return h.worker.calls() == 1 })
	})

	t.Run("an attempt that answers after its run was canceled elsewhere leaves it canceled", func(t *testing.T) {
		release := make(chan struct{})
		h := newHarness(t, open, aTask("render", provider.ConsumerSpec{Retry: retrying(3)}), func(*http.Request, *topicv1.Envelope, int) answer {
			<-release
			return answeredOK
		})
		working := h.engine(time.Second)
		h.serve(working, "render")
		id := h.trigger(working, "render", `{}`, nil)
		h.eventually("the attempt to start", func() bool { return h.worker.calls() == 1 })
		if _, err := h.engine(time.Second).Tasks().CancelRun(context.Background(), &taskv1.CancelRunRequest{Id: id}); err != nil {
			t.Fatalf("CancelRun: %v", err)
		}
		close(release)
		h.stillAfter(time.Second, "the run staying canceled", func() bool { return h.run("render", id).Status == provider.RunCanceled })
		for _, attempt := range h.reported() {
			if attempt.Execution == id {
				t.Errorf("an attempt of the canceled run was reported %s", attempt.Status)
			}
		}
	})

	t.Run("a delayed run starts no earlier than it is due", func(t *testing.T) {
		var mu sync.Mutex
		var startedAt time.Time
		h := newHarness(t, open, aTask("remind", provider.ConsumerSpec{Retry: retrying(3)}), func(*http.Request, *topicv1.Envelope, int) answer {
			mu.Lock()
			defer mu.Unlock()
			startedAt = time.Now()
			return answeredOK
		})
		engine := h.engine(time.Second)
		h.serve(engine, "remind")
		due := time.Now().Add(2 * time.Second)
		id := h.trigger(engine, "remind", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(due)})
		h.settlesAs("remind", id, provider.RunCompleted)
		mu.Lock()
		defer mu.Unlock()
		if startedAt.Before(due.Add(-300 * time.Millisecond)) {
			t.Errorf("the run started %v before it was due", due.Sub(startedAt))
		}
	})

	t.Run("a rescheduled run runs once, at its new time", func(t *testing.T) {
		h := newHarness(t, open, aTask("remind", provider.ConsumerSpec{Retry: retrying(3)}), answerAlways(answeredOK))
		engine := h.engine(time.Second)
		h.serve(engine, "remind")
		id := h.trigger(engine, "remind", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Hour))})
		if _, err := engine.Tasks().RescheduleRun(context.Background(), &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.Now()}); err != nil {
			t.Fatalf("RescheduleRun: %v", err)
		}
		h.settlesAs("remind", id, provider.RunCompleted)
		h.stillAfter(time.Second, "one call to the worker", func() bool { return h.worker.calls() == 1 })
	})

	t.Run("an idempotency key answers the run it first started", func(t *testing.T) {
		h := newHarness(t, open, aTask("resize", provider.ConsumerSpec{Retry: retrying(3)}), answerAlways(answeredOK))
		engine := h.engine(time.Second)
		h.serve(engine, "resize")
		first := h.trigger(engine, "resize", `{}`, &taskv1.TriggerOptions{IdempotencyKey: "order-1"})
		second := h.trigger(engine, "resize", `{}`, &taskv1.TriggerOptions{IdempotencyKey: "order-1"})
		if first != second {
			t.Fatalf("the second trigger started %s, want the first run %s", second, first)
		}
		h.settlesAs("resize", first, provider.RunCompleted)
		h.stillAfter(time.Second, "one call to the worker", func() bool { return h.worker.calls() == 1 })
	})

	t.Run("a schedule fires its task once a minute however often the minute is fired", func(t *testing.T) {
		h := newHarness(t, open, aTask("digest", provider.ConsumerSpec{Retry: retrying(3)}), answerAlways(answeredOK))
		engine := h.engine(time.Second)
		h.serve(engine, "digest")
		minute := time.Now().Truncate(time.Minute)
		for range 2 {
			if err := engine.FireCron(context.Background(), "digest", minute.Add(time.Second)); err != nil {
				t.Fatalf("FireCron: %v", err)
			}
		}
		h.eventually("the scheduled run to be attempted", func() bool { return h.worker.calls() == 1 })
		h.stillAfter(time.Second, "one call to the worker", func() bool { return h.worker.calls() == 1 })
		var payload map[string]string
		_ = json.Unmarshal(h.worker.delivered()[0].GetPayload(), &payload)
		if payload["timestamp"] != minute.UTC().Format(time.RFC3339) {
			t.Errorf("the scheduled run's payload names %q, want the minute %s", payload["timestamp"], minute.UTC().Format(time.RFC3339))
		}
	})

	t.Run("an attempt that outlives its lease is not delivered again", func(t *testing.T) {
		h := newHarness(t, open, aTask("export", provider.ConsumerSpec{Retry: retrying(3)}), func(*http.Request, *topicv1.Envelope, int) answer {
			time.Sleep(3500 * time.Millisecond)
			return answeredOK
		})
		engine := h.engineLeasing(time.Second, time.Second)
		h.serve(engine, "export")
		id := h.trigger(engine, "export", `{}`, nil)
		run := h.settlesAs("export", id, provider.RunCompleted)
		if run.Attempts != 1 || h.worker.calls() != 1 {
			t.Errorf("an attempt held 3.5 times its lease took %d attempts over %d calls, want 1 of each", run.Attempts, h.worker.calls())
		}
	})

	t.Run("an abort fails the run at once without retrying", func(t *testing.T) {
		h := newHarness(t, open, aTask("charge", provider.ConsumerSpec{Retry: retrying(3)}), answerAlways(answer{status: http.StatusBadRequest, body: `{"abort":{"reason":"card expired"}}`}))
		engine := h.engine(time.Second)
		h.serve(engine, "charge")
		id := h.trigger(engine, "charge", `{}`, nil)
		run := h.settlesAs("charge", id, provider.RunFailed)
		h.stillAfter(500*time.Millisecond, "one call to the worker", func() bool { return h.worker.calls() == 1 })
		if run.Attempts != 1 || run.Error != "card expired" {
			t.Errorf("the aborted run took %d attempts and failed with %q, want 1 and the abort's reason", run.Attempts, run.Error)
		}
	})

	t.Run("a task's attempt past its maxDuration times the run out without retrying", func(t *testing.T) {
		h := newHarness(t, open, aTask("render", provider.ConsumerSpec{Retry: retrying(3), MaxDuration: 200 * time.Millisecond}), blockUntilCanceled)
		engine := h.engine(time.Second)
		h.serve(engine, "render")
		id := h.trigger(engine, "render", `{}`, nil)
		run := h.settlesAs("render", id, provider.RunTimedOut)
		h.stillAfter(500*time.Millisecond, "one call to the worker", func() bool { return h.worker.calls() == 1 })
		if run.Attempts != 1 {
			t.Errorf("the timed-out run took %d attempts, want 1", run.Attempts)
		}
	})

	t.Run("triggers under one debounce key fold into the first run, due a delay after the last", func(t *testing.T) {
		h := newHarness(t, open, aTask("digest", provider.ConsumerSpec{Retry: retrying(3)}), answerAlways(answeredOK))
		engine := h.engine(time.Second)
		h.serve(engine, "digest")
		debounce := &taskv1.TriggerOptions{Debounce: &taskv1.Debounce{Key: "user-7", Delay: durationpb.New(1500 * time.Millisecond)}}
		first := h.trigger(engine, "digest", `{"n":1}`, debounce)
		time.Sleep(500 * time.Millisecond)
		last := time.Now()
		if second := h.trigger(engine, "digest", `{"n":2}`, debounce); second != first {
			t.Fatalf("a trigger inside the debounce window started %s, want the pending run %s", second, first)
		}
		h.settlesAs("digest", first, provider.RunCompleted)
		delivered := h.worker.delivered()
		if len(delivered) != 1 || payloadN(delivered[0]) != 1 {
			t.Fatalf("the worker was handed %d runs, want the one debounced run with the first trigger's payload", len(delivered))
		}
		if run := h.run("digest", first); run.StartedAt.Before(last.Add(1200 * time.Millisecond)) {
			t.Errorf("the debounced run started %v after the last trigger, want the 1.5s delay", run.StartedAt.Sub(last))
		}
		if after := h.trigger(engine, "digest", `{"n":3}`, debounce); after == first {
			t.Error("a trigger after the debounced run started joined it, want a new run")
		}
	})

	t.Run("concurrent triggers under one debounce key fold into one run, before and after the key's run started", func(t *testing.T) {
		release := make(chan struct{})
		h := newHarness(t, open, aTask("digest", provider.ConsumerSpec{Retry: retrying(3)}), func(r *http.Request, _ *topicv1.Envelope, _ int) answer {
			select {
			case <-release:
				return answeredOK
			case <-r.Context().Done():
				return answer{status: http.StatusServiceUnavailable}
			}
		})
		engine := h.engine(time.Second)
		h.serve(engine, "digest")
		debounce := &taskv1.TriggerOptions{Debounce: &taskv1.Debounce{Key: "user-9", Delay: durationpb.New(500 * time.Millisecond)}}
		const triggers = 8
		triggerAtOnce := func() []string {
			ids := make([]string, triggers)
			errs := make([]error, triggers)
			var ready, done sync.WaitGroup
			ready.Add(triggers)
			start := make(chan struct{})
			for i := range triggers {
				done.Go(func() {
					ready.Done()
					<-start
					resp, err := engine.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: "digest", Payload: []byte(`{}`), Options: debounce})
					ids[i], errs[i] = resp.GetId(), err
				})
			}
			ready.Wait()
			close(start)
			done.Wait()
			for i, err := range errs {
				if err != nil {
					t.Fatalf("Trigger %d: %v", i, err)
				}
			}
			return ids
		}
		countRuns := func() int64 {
			count, err := h.wiring.Store.CountRuns(context.Background(), runs.RunFilter{Topics: []string{"digest"}})
			if err != nil {
				t.Fatalf("CountRuns: %v", err)
			}
			return count
		}

		h.records.holdEnsuredUntil(triggers)
		first := triggerAtOnce()
		for _, id := range first {
			if id != first[0] {
				t.Fatalf("concurrent triggers on a fresh debounce key answered %v, want one run", first)
			}
		}
		if count := countRuns(); count != 1 {
			t.Fatalf("concurrent triggers on a fresh debounce key recorded %d runs, want 1", count)
		}

		h.eventually("the debounced run to start", func() bool { return h.worker.calls() == 1 })
		h.records.holdEnsuredUntil(triggers)
		second := triggerAtOnce()
		for _, id := range second {
			if id != second[0] || id == first[0] {
				t.Fatalf("concurrent triggers after the debounced run %s started answered %v, want one new run", first[0], second)
			}
		}
		if count := countRuns(); count != 2 {
			t.Fatalf("concurrent triggers after the debounced run started left %d runs, want 2", count)
		}
		close(release)
		h.settlesAs("digest", second[0], provider.RunCompleted)
		h.stillAfter(time.Second, "two calls to the worker", func() bool { return h.worker.calls() == 2 })
	})

	t.Run("a replay runs the same payload again as a new run", func(t *testing.T) {
		h := newHarness(t, open, aTask("resize", provider.ConsumerSpec{Retry: retrying(3)}), answerAlways(answeredOK))
		engine := h.engine(time.Second)
		h.serve(engine, "resize")
		id := h.trigger(engine, "resize", `{"n":4}`, &taskv1.TriggerOptions{Tags: []string{"eu"}})
		h.settlesAs("resize", id, provider.RunCompleted)
		replayed, err := engine.Tasks().ReplayRun(context.Background(), &taskv1.ReplayRunRequest{Id: id})
		if err != nil || replayed.GetId() == id {
			t.Fatalf("ReplayRun = %v, %v, want a new run", replayed.GetId(), err)
		}
		run := h.settlesAs("resize", replayed.GetId(), provider.RunCompleted)
		if string(run.Payload) != `{"n":4}` || !slices.Equal(run.Tags, []string{"eu"}) || h.worker.calls() != 2 {
			t.Errorf("the replay holds payload %s and tags %v after %d calls, want the original's over 2 calls", run.Payload, run.Tags, h.worker.calls())
		}
	})

	t.Run("messages of one key run one at a time in the order they were sent, a retry included", func(t *testing.T) {
		h := newHarness(t, open, aTopic("ledger", true, provider.ConsumerSpec{Name: "apply", Retry: retrying(3)}), func(_ *http.Request, delivered *topicv1.Envelope, _ int) answer {
			time.Sleep(20 * time.Millisecond)
			if payloadN(delivered) == 0 && delivered.GetAttempt().GetNumber() == 1 {
				return answer{status: http.StatusInternalServerError}
			}
			return answeredOK
		})
		engine := h.engine(time.Second)
		var ids []string
		for n := range 5 {
			ids = append(ids, h.send(engine, "ledger", "account-1", fmt.Sprintf(`{"n":%d}`, n)))
		}
		h.serve(engine, "ledger")
		for _, id := range ids {
			h.settlesAs("ledger", runs.ExecutionOf(id, "apply"), provider.RunCompleted)
		}
		var order []int
		for _, delivered := range h.worker.delivered() {
			order = append(order, payloadN(delivered))
		}
		if !slices.Equal(order, []int{0, 0, 1, 2, 3, 4}) || h.worker.mostAtOnce() != 1 {
			t.Errorf("the worker was handed %v with up to %d at once, want [0 0 1 2 3 4] one at a time", order, h.worker.mostAtOnce())
		}
	})

	t.Run("a message reaches each consumer once, and its dead letter is redriven", func(t *testing.T) {
		failBilling := true
		var mu sync.Mutex
		h := newHarness(t, open, aTopic("orders", false,
			provider.ConsumerSpec{Name: "ship", Retry: retrying(3)},
			provider.ConsumerSpec{Name: "bill", Retry: retrying(1)},
		), func(_ *http.Request, delivered *topicv1.Envelope, _ int) answer {
			mu.Lock()
			defer mu.Unlock()
			if delivered.GetConsumer() == "bill" && failBilling {
				return answer{status: http.StatusInternalServerError, body: "card declined"}
			}
			return answeredOK
		})
		engine := h.engine(time.Second)
		h.serve(engine, "orders")
		id := h.send(engine, "orders", "", `{"n":7}`)
		h.settlesAs("orders", runs.ExecutionOf(id, "ship"), provider.RunCompleted)
		h.settlesAs("orders", runs.ExecutionOf(id, "bill"), provider.RunFailed)
		ctx := context.Background()
		letters, err := engine.Topics().ListDeadLetters(ctx, &topicv1.ListDeadLettersRequest{Topic: "orders", Consumer: "bill"})
		if err != nil {
			t.Fatal(err)
		}
		if len(letters.GetDeadLetters()) != 1 || letters.GetDeadLetters()[0].GetMessage().GetId() != id || string(letters.GetDeadLetters()[0].GetPayload()) != `{"n":7}` {
			t.Fatalf("dead letters = %v, want message %s with its payload", letters.GetDeadLetters(), id)
		}
		counted, err := engine.Topics().CountDeadLetters(ctx, &topicv1.CountDeadLettersRequest{Topic: "orders", Consumer: "bill"})
		if err != nil || counted.GetCount() != 1 {
			t.Fatalf("CountDeadLetters = %d, %v, want 1", counted.GetCount(), err)
		}
		mu.Lock()
		failBilling = false
		mu.Unlock()
		redriven, err := engine.Topics().RedriveDeadLetters(ctx, &topicv1.RedriveDeadLettersRequest{Topic: "orders", Consumer: "bill"})
		if err != nil || redriven.GetRedriven() != 1 {
			t.Fatalf("RedriveDeadLetters = %d, %v, want 1", redriven.GetRedriven(), err)
		}
		h.settlesAs("orders", runs.ExecutionOf(id, "bill"), provider.RunCompleted)
		perConsumer := map[string]int{}
		for _, delivered := range h.worker.delivered() {
			perConsumer[delivered.GetConsumer()]++
		}
		if perConsumer["ship"] != 1 || perConsumer["bill"] != 2 {
			t.Errorf("the worker was handed %v, want ship once and bill twice", perConsumer)
		}
	})

	t.Run("a batching consumer is handed envelopes of at most its batch size", func(t *testing.T) {
		h := newHarness(t, open, aTopic("clicks", false, provider.ConsumerSpec{Name: "count", Retry: retrying(3), Batch: &provider.BatchPolicy{Size: 3, Timeout: 500 * time.Millisecond}}), answerAlways(answeredOK))
		engine := h.engine(time.Second)
		var ids []string
		for n := range 7 {
			ids = append(ids, h.send(engine, "clicks", "", fmt.Sprintf(`{"n":%d}`, n)))
		}
		h.serve(engine, "clicks")
		for _, id := range ids {
			h.settlesAs("clicks", runs.ExecutionOf(id, "count"), provider.RunCompleted)
		}
		total := 0
		for _, delivered := range h.worker.delivered() {
			if size := len(delivered.GetMessages()); size < 1 || size > 3 {
				t.Errorf("an envelope held %d messages, want 1 to 3", size)
			}
			total += len(delivered.GetMessages())
		}
		if total != 7 {
			t.Errorf("the worker was handed %d messages, want 7", total)
		}
	})

	t.Run("a consumer with a concurrency of one never runs two attempts at once", func(t *testing.T) {
		h := newHarness(t, open, aTopic("emails", false, provider.ConsumerSpec{Name: "send", Retry: retrying(3), Concurrency: 1}), func(*http.Request, *topicv1.Envelope, int) answer {
			time.Sleep(30 * time.Millisecond)
			return answeredOK
		})
		engine := h.engine(time.Second)
		var ids []string
		for n := range 4 {
			ids = append(ids, h.send(engine, "emails", "", fmt.Sprintf(`{"n":%d}`, n)))
		}
		h.serve(engine, "emails")
		for _, id := range ids {
			h.settlesAs("emails", runs.ExecutionOf(id, "send"), provider.RunCompleted)
		}
		if most := h.worker.mostAtOnce(); most != 1 {
			t.Errorf("the worker ran %d attempts at once, want 1", most)
		}
	})
}
