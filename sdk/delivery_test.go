package ocel_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ocel.dev"
)

const messageID = "01HZY4B6Q0Z0Z0Z0Z0Z0Z0Z0Z0"

func envelope(topic, consumer string, attempt, of int, payload string) string {
	return fmt.Sprintf(`{"v":1,"topic":%q,"consumer":%q,"execution":"%s-%s",`+
		`"message":{"id":%q,"publishedAt":"2030-01-01T00:00:00Z"},`+
		`"attempt":{"number":%d,"of":%d,"firstAttemptedAt":"2030-01-01T00:00:01Z"},"payload":%s}`,
		topic, consumer, messageID, consumer, messageID, attempt, of, payload)
}

func deliver(t *testing.T, worker, body string) (int, string) {
	t.Helper()
	return deliverWith(t.Context(), worker, body)
}

func deliverWith(ctx context.Context, worker, body string) (int, string) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(body))
	ocel.WorkerHandler(worker).ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestADeliveryThatIsNotAnEnvelopeIsRefusedWith400(t *testing.T) {
	status, body := deliver(t, "worker", "not json")

	if status != http.StatusBadRequest || body == "" {
		t.Errorf("answer = %d %q, want 400 with a reason", status, body)
	}
}

func TestADeliveryToAConsumerThisAppDoesNotDeclareIsRefusedWith404(t *testing.T) {
	status, body := deliver(t, "worker", envelope("nowhere", "nobody", 1, 3, "null"))

	if status != http.StatusNotFound || !strings.Contains(body, `"nobody"`) || !strings.Contains(body, `"nowhere"`) {
		t.Errorf("answer = %d %q, want 404 naming the topic and consumer", status, body)
	}
}

func TestADeliveryToATaskOfAnotherWorkerIsRefusedWith404NamingBoth(t *testing.T) {
	media := ocel.Worker("media-elsewhere")
	ocel.Task("resize-elsewhere", resize, ocel.UseWorker(media))

	status, body := deliver(t, "worker", envelope("resize-elsewhere", "resize-elsewhere", 1, 3, `{}`))

	if status != http.StatusNotFound || !strings.Contains(body, `"media-elsewhere"`) || !strings.Contains(body, `"worker"`) {
		t.Errorf("answer = %d %q, want 404 naming both workers", status, body)
	}
}

func TestASucceedingTaskAnswers200WithItsOutputAsJSON(t *testing.T) {
	ocel.Task("resize-succeeds", func(_ context.Context, in image) (thumbnail, error) {
		return thumbnail{URL: "https://cdn/" + in.Key}, nil
	})

	status, body := deliver(t, "worker", envelope("resize-succeeds", "resize-succeeds", 1, 3, `{"key":"a.png"}`))

	if status != http.StatusOK || body != `{"url":"https://cdn/a.png"}` {
		t.Errorf("answer = %d %q", status, body)
	}
}

func TestASucceedingConsumerAnswers200WithNull(t *testing.T) {
	var got order
	ocel.Topic[order]("orders-succeed").Consumer("audit", func(_ context.Context, in order) error {
		got = in
		return nil
	})

	status, body := deliver(t, "worker", envelope("orders-succeed", "audit", 1, 3, `{"id":"o-1","total":5}`))

	if status != http.StatusOK || body != "null" || got != (order{ID: "o-1", Total: 5}) {
		t.Errorf("answer = %d %q after handling %+v", status, body, got)
	}
}

func TestARunThatReturnsErrAbortAnswers422WithTheAbortBody(t *testing.T) {
	ocel.Task("resize-aborts", func(context.Context, image) (thumbnail, error) {
		return thumbnail{}, fmt.Errorf("not a <png>: %w", ocel.ErrAbort)
	})

	status, body := deliver(t, "worker", envelope("resize-aborts", "resize-aborts", 1, 3, `{}`))

	if want := `{"abort":{"reason":"not a <png>: aborted without retrying"}}`; status != http.StatusUnprocessableEntity || body != want {
		t.Errorf("answer = %d %q, want 422 %q", status, body, want)
	}
}

func TestARunThatFailsAnswers500WithTheErrorMessage(t *testing.T) {
	ocel.Topic[order]("orders-fail").Consumer("audit", func(context.Context, order) error {
		return errors.New("the ledger is down")
	})

	status, body := deliver(t, "worker", envelope("orders-fail", "audit", 1, 3, `{}`))

	if status != http.StatusInternalServerError || body != "the ledger is down" {
		t.Errorf("answer = %d %q", status, body)
	}
}

func TestARunThatPanicsAnswers500(t *testing.T) {
	ocel.Task("resize-panics", func(context.Context, image) (thumbnail, error) { panic("out of memory") })

	status, body := deliver(t, "worker", envelope("resize-panics", "resize-panics", 1, 3, `{}`))

	if status != http.StatusInternalServerError || !strings.Contains(body, "out of memory") {
		t.Errorf("answer = %d %q", status, body)
	}
}

func TestCatchErrorThatSkipsRetryingTurnsAFailureIntoAnAbort(t *testing.T) {
	var caught error
	ocel.Task("resize-caught", func(context.Context, image) (thumbnail, error) {
		return thumbnail{}, errors.New("quota exceeded")
	}, ocel.CatchError(func(_ context.Context, _ image, err error) bool {
		caught = err
		return true
	}))

	status, body := deliver(t, "worker", envelope("resize-caught", "resize-caught", 1, 3, `{}`))

	if status != http.StatusUnprocessableEntity || body != `{"abort":{"reason":"quota exceeded"}}` || caught == nil {
		t.Errorf("answer = %d %q after catching %v", status, body, caught)
	}
}

func TestCatchErrorThatKeepsRetryingLeavesTheFailureToBeRetried(t *testing.T) {
	ocel.Task("resize-caught-retried", func(context.Context, image) (thumbnail, error) {
		return thumbnail{}, errors.New("quota exceeded")
	}, ocel.CatchError(func(context.Context, image, error) bool { return false }))

	status, body := deliver(t, "worker", envelope("resize-caught-retried", "resize-caught-retried", 1, 3, `{}`))

	if status != http.StatusInternalServerError || body != "quota exceeded" {
		t.Errorf("answer = %d %q", status, body)
	}
}

func TestAPayloadThatDoesNotDecodeIntoTheTypeAborts(t *testing.T) {
	ran := false
	ocel.Topic[order]("orders-undecodable").Consumer("audit", func(context.Context, order) error {
		ran = true
		return nil
	})

	status, body := deliver(t, "worker", envelope("orders-undecodable", "audit", 1, 3, `{"total":"lots"}`))

	if status != http.StatusUnprocessableEntity || !strings.Contains(body, "ocel_test.order") || ran {
		t.Errorf("answer = %d %q, ran = %v", status, body, ran)
	}
}

func TestABatchConsumerIsHandedEveryPayloadOfTheBatch(t *testing.T) {
	var got []order
	var run ocel.RunContext
	ocel.Topic[order]("orders-batch").BatchConsumer("ledger", 10, func(ctx context.Context, in []order) error {
		got = in
		run, _ = ocel.RunFrom(ctx)
		return nil
	})

	status, body := deliver(t, "worker", `{"v":1,"topic":"orders-batch","consumer":"ledger","messages":[`+
		`{"execution":"e-1","message":{"id":"m-1"},"attempt":{"number":2,"of":5},"payload":{"id":"a"}},`+
		`{"execution":"e-2","message":{"id":"m-2"},"attempt":{"number":1,"of":5},"payload":{"id":"b"}}]}`)

	if status != http.StatusOK || !slices.Equal(got, []order{{ID: "a"}, {ID: "b"}}) {
		t.Errorf("answer = %d %q after handling %v", status, body, got)
	}
	if run.ID != "e-1" || run.Message.ID != "m-1" || run.Attempt.Number != 2 || run.Attempt.Of != 5 {
		t.Errorf("run = %+v, want the first message's", run)
	}
}

func TestAConsumerOfSingleMessagesRefusesABatchWith400(t *testing.T) {
	ocel.Topic[order]("orders-unbatched").Consumer("audit", func(context.Context, order) error { return nil })

	status, _ := deliver(t, "worker", `{"v":1,"topic":"orders-unbatched","consumer":"audit","messages":[{"execution":"e-1","payload":{}}]}`)

	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
}

func TestRunFromReadsTheAttemptTheEnvelopeDescribes(t *testing.T) {
	var run ocel.RunContext
	var ok bool
	ocel.Task("resize-context", func(ctx context.Context, _ image) (thumbnail, error) {
		run, ok = ocel.RunFrom(ctx)
		return thumbnail{}, nil
	})

	deliver(t, "worker", envelope("resize-context", "resize-context", 2, 3, `{}`))

	want := ocel.RunContext{
		Kind:  ocel.KindTask,
		Name:  "resize-context",
		Topic: "resize-context",
		ID:    messageID + "-resize-context",
		Attempt: ocel.Attempt{
			Number:           2,
			Of:               3,
			FirstAttemptedAt: time.Date(2030, 1, 1, 0, 0, 1, 0, time.UTC),
		},
		Message: ocel.Message{ID: messageID, PublishedAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	if !ok || run != want {
		t.Errorf("RunFrom() = %+v, %v, want %+v", run, ok, want)
	}
	if _, ok := ocel.RunFrom(t.Context()); ok {
		t.Error("RunFrom() found a run in a context no delivery made")
	}
}

func TestAnAttemptRunsWorkerMiddlewareThenOnStartAttemptThenTaskMiddlewareThenTheRun(t *testing.T) {
	var order []string
	note := func(step string) { order = append(order, step) }
	ordering := ocel.Worker("ordering", ocel.Middleware(func(ctx context.Context, next func(context.Context) error) error {
		run, _ := ocel.RunFrom(ctx)
		note("worker middleware sees " + string(run.Kind))
		return next(ctx)
	}))
	ocel.Task("resize-ordering", func(context.Context, image) (thumbnail, error) {
		note("run")
		return thumbnail{}, nil
	},
		ocel.UseWorker(ordering),
		ocel.OnStartAttempt(func(context.Context, image) error { note("onStartAttempt"); return nil }),
		ocel.Middleware(func(ctx context.Context, next func(context.Context) error) error {
			note("task middleware")
			return next(ctx)
		}),
	)

	status, _ := deliver(t, "ordering", envelope("resize-ordering", "resize-ordering", 1, 3, `{}`))

	want := []string{"worker middleware sees task", "onStartAttempt", "task middleware", "run"}
	if status != http.StatusOK || !slices.Equal(order, want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestAWorkersMiddlewareWrapsItsConsumersToo(t *testing.T) {
	var kinds []ocel.RunKind
	audits := ocel.Worker("audits", ocel.Middleware(func(ctx context.Context, next func(context.Context) error) error {
		run, _ := ocel.RunFrom(ctx)
		kinds = append(kinds, run.Kind)
		return next(ctx)
	}))
	ocel.Topic[order]("orders-wrapped").Consumer("audit", func(context.Context, order) error { return nil }, ocel.UseWorker(audits))

	deliver(t, "audits", envelope("orders-wrapped", "audit", 1, 3, `{}`))

	if !slices.Equal(kinds, []ocel.RunKind{ocel.KindConsumer}) {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestAnErrorFromOnStartAttemptFailsTheAttempt(t *testing.T) {
	ran := false
	ocel.Task("resize-start-attempt-fails", func(context.Context, image) (thumbnail, error) {
		ran = true
		return thumbnail{}, nil
	}, ocel.OnStartAttempt(func(context.Context, image) error { return errors.New("not ready") }))

	status, body := deliver(t, "worker", envelope("resize-start-attempt-fails", "resize-start-attempt-fails", 1, 3, `{}`))

	if status != http.StatusInternalServerError || body != "not ready" || ran {
		t.Errorf("answer = %d %q, ran = %v", status, body, ran)
	}
}

type lifecycle struct {
	mu    sync.Mutex
	calls []string
}

func (l *lifecycle) note(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *lifecycle) options() []ocel.TaskOption {
	return []ocel.TaskOption{
		ocel.OnSuccess(func(_ context.Context, in image, out thumbnail) error {
			l.note("success " + in.Key + " " + out.URL)
			return errors.New("the hook itself failed")
		}),
		ocel.OnFailure(func(_ context.Context, in image, err error) error {
			l.note("failure " + in.Key + " " + err.Error())
			return nil
		}),
		ocel.OnComplete(func(_ context.Context, _ image, result ocel.RunResult[thumbnail]) error {
			if result.Err != nil {
				l.note("complete failed " + result.Err.Error())
			} else {
				l.note("complete " + result.Output.URL)
			}
			return nil
		}),
		ocel.OnCancel(func(_ context.Context, in image) error {
			l.note("cancel " + in.Key)
			return nil
		}),
	}
}

func TestASucceedingRunCallsOnSuccessThenOnCompleteAndAHookErrorChangesNothing(t *testing.T) {
	hooks := &lifecycle{}
	ocel.Task("resize-lifecycle-success", func(context.Context, image) (thumbnail, error) {
		return thumbnail{URL: "u"}, nil
	}, hooks.options()...)

	status, body := deliver(t, "worker", envelope("resize-lifecycle-success", "resize-lifecycle-success", 1, 3, `{"key":"k"}`))

	if status != http.StatusOK || body != `{"url":"u"}` {
		t.Errorf("answer = %d %q", status, body)
	}
	if want := []string{"success k u", "complete u"}; !slices.Equal(hooks.calls, want) {
		t.Errorf("calls = %v, want %v", hooks.calls, want)
	}
}

func TestAFailureWithAttemptsLeftCallsNoLifecycleHook(t *testing.T) {
	hooks := &lifecycle{}
	ocel.Task("resize-lifecycle-retry", func(context.Context, image) (thumbnail, error) {
		return thumbnail{}, errors.New("flaky")
	}, hooks.options()...)

	status, _ := deliver(t, "worker", envelope("resize-lifecycle-retry", "resize-lifecycle-retry", 2, 3, `{"key":"k"}`))

	if status != http.StatusInternalServerError || len(hooks.calls) != 0 {
		t.Errorf("status = %d, calls = %v, want a retry and no hook", status, hooks.calls)
	}
}

func TestTheLastFailingAttemptCallsOnFailureThenOnComplete(t *testing.T) {
	hooks := &lifecycle{}
	ocel.Task("resize-lifecycle-failure", func(context.Context, image) (thumbnail, error) {
		return thumbnail{}, errors.New("flaky")
	}, hooks.options()...)

	status, _ := deliver(t, "worker", envelope("resize-lifecycle-failure", "resize-lifecycle-failure", 3, 3, `{"key":"k"}`))

	if want := []string{"failure k flaky", "complete failed flaky"}; status != http.StatusInternalServerError || !slices.Equal(hooks.calls, want) {
		t.Errorf("status = %d, calls = %v, want %v", status, hooks.calls, want)
	}
}

func TestAnAbortedRunIsTerminalOnItsFirstAttempt(t *testing.T) {
	hooks := &lifecycle{}
	ocel.Task("resize-lifecycle-abort", func(context.Context, image) (thumbnail, error) {
		return thumbnail{}, ocel.ErrAbort
	}, hooks.options()...)

	status, _ := deliver(t, "worker", envelope("resize-lifecycle-abort", "resize-lifecycle-abort", 1, 3, `{"key":"k"}`))

	want := []string{"failure k aborted without retrying", "complete failed aborted without retrying"}
	if status != http.StatusUnprocessableEntity || !slices.Equal(hooks.calls, want) {
		t.Errorf("status = %d, calls = %v, want %v", status, hooks.calls, want)
	}
}

func TestACancelledDeliveryCancelsTheRunsContextAndCallsOnCancel(t *testing.T) {
	hooks := &lifecycle{}
	started := make(chan struct{})
	ocel.Task("resize-cancelled", func(ctx context.Context, _ image) (thumbnail, error) {
		close(started)
		<-ctx.Done()
		return thumbnail{}, ctx.Err()
	}, hooks.options()...)

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()
	status, _ := deliverWith(ctx, "worker", envelope("resize-cancelled", "resize-cancelled", 3, 3, `{"key":"k"}`))

	if status != http.StatusInternalServerError || !slices.Equal(hooks.calls, []string{"cancel k"}) {
		t.Errorf("status = %d, calls = %v, want only the cancel hook", status, hooks.calls)
	}
}

func TestOnStartRunsOnceBeforeTheFirstDeliveriesWhichWaitForIt(t *testing.T) {
	var starts atomic.Int32
	release := make(chan struct{})
	booting := ocel.Worker("booting", ocel.OnStart(func(context.Context) error {
		starts.Add(1)
		<-release
		return nil
	}))
	var handled atomic.Int32
	ocel.Topic[order]("orders-booting").Consumer("audit", func(context.Context, order) error {
		handled.Add(1)
		return nil
	}, ocel.UseWorker(booting))

	var wg sync.WaitGroup
	statuses := make([]int, 5)
	for i := range statuses {
		wg.Go(func() {
			statuses[i], _ = deliver(t, "booting", envelope("orders-booting", "audit", 1, 3, `{}`))
		})
	}
	time.Sleep(20 * time.Millisecond)
	if handled.Load() != 0 {
		t.Errorf("handled %d deliveries before the worker started", handled.Load())
	}
	close(release)
	wg.Wait()

	if starts.Load() != 1 || handled.Load() != 5 || slices.ContainsFunc(statuses, func(s int) bool { return s != http.StatusOK }) {
		t.Errorf("starts = %d, handled = %d, statuses = %v", starts.Load(), handled.Load(), statuses)
	}
}

func TestAFailingOnStartFailsTheDeliveryAndIsRetriedOnTheNext(t *testing.T) {
	var starts atomic.Int32
	flaky := ocel.Worker("flaky-start", ocel.OnStart(func(context.Context) error {
		if starts.Add(1) == 1 {
			return errors.New("the database is not up")
		}
		return nil
	}))
	ocel.Topic[order]("orders-flaky-start").Consumer("audit", func(context.Context, order) error { return nil }, ocel.UseWorker(flaky))

	first, body := deliver(t, "flaky-start", envelope("orders-flaky-start", "audit", 1, 3, `{}`))
	second, _ := deliver(t, "flaky-start", envelope("orders-flaky-start", "audit", 2, 3, `{}`))
	third, _ := deliver(t, "flaky-start", envelope("orders-flaky-start", "audit", 3, 3, `{}`))

	if first != http.StatusInternalServerError || !strings.Contains(body, "the database is not up") {
		t.Errorf("first answer = %d %q, want 500 with the start error", first, body)
	}
	if second != http.StatusOK || third != http.StatusOK || starts.Load() != 2 {
		t.Errorf("then %d and %d after %d starts, want two 200s after 2", second, third, starts.Load())
	}
}

func TestAWorkersConcurrencyCapsTheRunsInFlightAcrossItsTasksAndConsumers(t *testing.T) {
	capped := ocel.Worker("capped", ocel.Concurrency(2))
	var inFlight, peak atomic.Int32
	hold := func() {
		now := inFlight.Add(1)
		for {
			seen := peak.Load()
			if now <= seen || peak.CompareAndSwap(seen, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		inFlight.Add(-1)
	}
	ocel.Task("resize-capped", func(context.Context, image) (thumbnail, error) {
		hold()
		return thumbnail{}, nil
	}, ocel.UseWorker(capped))
	ocel.Topic[order]("orders-capped").Consumer("audit", func(context.Context, order) error {
		hold()
		return nil
	}, ocel.UseWorker(capped))

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Go(func() {
			if i%2 == 0 {
				deliver(t, "capped", envelope("resize-capped", "resize-capped", 1, 3, `{}`))
				return
			}
			deliver(t, "capped", envelope("orders-capped", "audit", 1, 3, `{}`))
		})
	}
	wg.Wait()

	if peak.Load() != 2 {
		t.Errorf("peak in flight = %d, want the worker's cap of 2", peak.Load())
	}
}

func TestRunFromReportsAttemptOneOfOneWhenTheEnvelopeCarriesNoAttempt(t *testing.T) {
	var run ocel.RunContext
	ocel.Task("resize-no-attempt", func(ctx context.Context, _ image) (thumbnail, error) {
		run, _ = ocel.RunFrom(ctx)
		return thumbnail{}, nil
	})

	deliver(t, "worker", `{"v":1,"topic":"resize-no-attempt","consumer":"resize-no-attempt","execution":"e-1","payload":{}}`)

	if run.Attempt.Number != 1 || run.Attempt.Of != 1 {
		t.Errorf("attempt = %+v, want 1 of 1", run.Attempt)
	}
}

func TestAFailureInAnEnvelopeWithoutAnAttemptIsTheLastAttempt(t *testing.T) {
	hooks := &lifecycle{}
	ocel.Task("resize-no-attempt-fails", func(context.Context, image) (thumbnail, error) {
		return thumbnail{}, errors.New("flaky")
	}, hooks.options()...)

	status, _ := deliver(t, "worker", `{"v":1,"topic":"resize-no-attempt-fails","consumer":"resize-no-attempt-fails","execution":"e-1","payload":{"key":"k"}}`)

	if want := []string{"failure k flaky", "complete failed flaky"}; status != http.StatusInternalServerError || !slices.Equal(hooks.calls, want) {
		t.Errorf("status = %d, calls = %v, want %v", status, hooks.calls, want)
	}
}

func TestABatchRunReadsTheFirstDeliverysAttemptBeforeTheEnvelopes(t *testing.T) {
	var run ocel.RunContext
	ocel.Topic[order]("orders-batch-attempt").BatchConsumer("ledger", 10, func(ctx context.Context, _ []order) error {
		run, _ = ocel.RunFrom(ctx)
		return nil
	})

	deliver(t, "worker", `{"v":1,"topic":"orders-batch-attempt","consumer":"ledger","attempt":{"number":4,"of":9},"messages":[`+
		`{"execution":"e-1","attempt":{"number":2,"of":5},"payload":{}}]}`)

	if run.Attempt.Number != 2 || run.Attempt.Of != 5 {
		t.Errorf("attempt = %+v, want the first delivery's 2 of 5", run.Attempt)
	}
}

func TestABatchRunWithNoAttemptAnywhereIsAttemptOneOfOne(t *testing.T) {
	var run ocel.RunContext
	ocel.Topic[order]("orders-batch-no-attempt").BatchConsumer("ledger", 10, func(ctx context.Context, _ []order) error {
		run, _ = ocel.RunFrom(ctx)
		return nil
	})

	deliver(t, "worker", `{"v":1,"topic":"orders-batch-no-attempt","consumer":"ledger","messages":[{"execution":"e-1","payload":{}}]}`)

	if run.Attempt.Number != 1 || run.Attempt.Of != 1 {
		t.Errorf("attempt = %+v, want 1 of 1", run.Attempt)
	}
}

func TestABatchConsumerTreatsASinglePayloadEnvelopeAsABatchOfOne(t *testing.T) {
	var got []order
	ocel.Topic[order]("orders-batch-of-one").BatchConsumer("ledger", 10, func(_ context.Context, in []order) error {
		got = in
		return nil
	})

	status, _ := deliver(t, "worker", envelope("orders-batch-of-one", "ledger", 1, 3, `{"id":"a"}`))

	if status != http.StatusOK || !slices.Equal(got, []order{{ID: "a"}}) {
		t.Errorf("status = %d after handling %v, want a batch of one", status, got)
	}
}

func TestAPayloadThatDoesNotDecodeCallsNoHook(t *testing.T) {
	hooks := &lifecycle{}
	ocel.Task("resize-undecodable", resize, hooks.options()...)

	status, _ := deliver(t, "worker", envelope("resize-undecodable", "resize-undecodable", 3, 3, `{"key":5}`))

	if status != http.StatusUnprocessableEntity || len(hooks.calls) != 0 {
		t.Errorf("status = %d, calls = %v, want 422 and no hook", status, hooks.calls)
	}
}

func TestAnOutputThatDoesNotEncodeAbortsAndCallsOnFailureThenOnComplete(t *testing.T) {
	var calls []string
	ocel.Task("resize-unencodable", func(context.Context, image) (func(), error) { return func() {}, nil },
		ocel.OnFailure(func(context.Context, image, error) error { calls = append(calls, "failure"); return nil }),
		ocel.OnComplete(func(context.Context, image, ocel.RunResult[func()]) error {
			calls = append(calls, "complete")
			return nil
		}),
	)

	status, body := deliver(t, "worker", envelope("resize-unencodable", "resize-unencodable", 1, 3, `{}`))

	if status != http.StatusUnprocessableEntity || !strings.HasPrefix(body, `{"abort":{"reason":`) || !slices.Equal(calls, []string{"failure", "complete"}) {
		t.Errorf("answer = %d %q, calls = %v", status, body, calls)
	}
}

func cancelWhileRunning(t *testing.T, name string, finish func() (thumbnail, error), hooks *lifecycle) (int, string) {
	t.Helper()
	started := make(chan struct{})
	ocel.Task(name, func(ctx context.Context, _ image) (thumbnail, error) {
		close(started)
		<-ctx.Done()
		return finish()
	}, hooks.options()...)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()
	return deliverWith(ctx, "worker", envelope(name, name, 3, 3, `{"key":"k"}`))
}

func TestACancelledRunThatReturnsItsOutputAnswers200AndCallsOnlyOnCancel(t *testing.T) {
	hooks := &lifecycle{}

	status, body := cancelWhileRunning(t, "resize-cancelled-succeeds", func() (thumbnail, error) {
		return thumbnail{URL: "u"}, nil
	}, hooks)

	if status != http.StatusOK || body != `{"url":"u"}` || !slices.Equal(hooks.calls, []string{"cancel k"}) {
		t.Errorf("answer = %d %q, calls = %v, want 200 with the output and only the cancel hook", status, body, hooks.calls)
	}
}

func TestACancelledRunThatAbortsAnswers422AndCallsOnlyOnCancel(t *testing.T) {
	hooks := &lifecycle{}

	status, body := cancelWhileRunning(t, "resize-cancelled-aborts", func() (thumbnail, error) {
		return thumbnail{}, ocel.ErrAbort
	}, hooks)

	if status != http.StatusUnprocessableEntity || body != `{"abort":{"reason":"aborted without retrying"}}` || !slices.Equal(hooks.calls, []string{"cancel k"}) {
		t.Errorf("answer = %d %q, calls = %v, want 422 and only the cancel hook", status, body, hooks.calls)
	}
}
