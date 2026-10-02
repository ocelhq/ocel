package tasks

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const exactJSON = `{"ratio":2.0,"id":9007199254740993}`

type taskOption func(*provider.TopicSpec)

func aTask(name string, options ...taskOption) *provider.TopicSpec {
	topic := &provider.TopicSpec{Consumers: []provider.ConsumerSpec{{Name: name, Worker: "worker", Exclusive: true, Retry: provider.ResolveRetryPolicy()}}}
	for _, option := range options {
		option(topic)
	}
	return topic
}

func retrying(attempts int, minDelay, maxDelay time.Duration) taskOption {
	return func(topic *provider.TopicSpec) {
		for i := range topic.Consumers {
			topic.Consumers[i].Retry = provider.RetryPolicy{MaxAttempts: attempts, MinDelay: minDelay, MaxDelay: maxDelay}
		}
	}
}

func outliving(maxDuration time.Duration) taskOption {
	return func(topic *provider.TopicSpec) {
		topic.Consumers[0].MaxDuration = maxDuration
	}
}

func ordered(topic *provider.TopicSpec) { topic.Ordered = true }

func trigger(t *testing.T, e *Engine, task, payload string, options *taskv1.TriggerOptions) string {
	t.Helper()
	resp, err := e.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: task, Payload: []byte(payload), Options: options})
	if err != nil {
		t.Fatalf("Trigger(%s): %v", task, err)
	}
	return resp.GetId()
}

func retrieve(t *testing.T, e *Engine, id string) *taskv1.Run {
	t.Helper()
	resp, err := e.Tasks().RetrieveRun(context.Background(), &taskv1.RetrieveRunRequest{Id: id})
	if err != nil {
		t.Fatalf("RetrieveRun(%s): %v", id, err)
	}
	return resp.GetRun()
}

func TestLiveATriggeredRunIsDeliveredToItsWorkerAndCompletesWithItsOutput(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize")}, nil)
	worker := newFakeWorker(t, func(*topicv1.Envelope) answer { return answer{status: http.StatusOK, body: exactJSON} })
	e := d.engine(worker)

	id := trigger(t, e, "resize", exactJSON, &taskv1.TriggerOptions{Tags: []string{"probe"}})
	if run := retrieve(t, e, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_QUEUED || run.GetTask() != "resize" {
		t.Fatalf("a fresh run = %v, want it queued under the task resize", run)
	}
	if failed := deliver(t, d, e, "resize", "resize", d.receive(t, "resize", "resize", 10*time.Second)); len(failed) != 0 {
		t.Fatalf("the delivery kept %v on the queue, want it acknowledged", failed)
	}

	run := retrieve(t, e, id)
	if run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED || run.GetAttempts() != 1 {
		t.Fatalf("run = %v, want it completed on its first attempt", run)
	}
	if string(run.GetOutput()) != exactJSON || string(run.GetPayload()) != exactJSON {
		t.Errorf("run payload %s output %s, want both %s byte for byte", run.GetPayload(), run.GetOutput(), exactJSON)
	}
	got := worker.deliveries()
	if len(got) != 1 {
		t.Fatalf("the worker received %d envelopes, want 1", len(got))
	}
	envelope := got[0].envelope
	if envelope.GetTopic() != "resize" || envelope.GetConsumer() != "resize" || envelope.GetExecution() != id ||
		envelope.GetAttempt().GetNumber() != 1 || envelope.GetAttempt().GetOf() != 3 || len(envelope.GetMessage().GetId()) != 26 {
		t.Errorf("envelope = %v, want topic and consumer resize, execution %s, attempt 1 of 3 and the message id", envelope, id)
	}
	if payload := rawField(t, got[0].body, "payload"); payload != exactJSON {
		t.Errorf("the envelope's payload = %s, want %s inline and unchanged", payload, exactJSON)
	}
	d.quiet(t, "resize", "resize", time.Second)
}

func TestLiveAFailedAttemptIsQueuedAgainAfterItsBackoffUntilOneSucceeds(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize", retrying(3, 2*time.Second, 2*time.Second))}, nil)
	worker := newFakeWorker(t, func(envelope *topicv1.Envelope) answer {
		if envelope.GetAttempt().GetNumber() < 2 {
			return answer{status: http.StatusInternalServerError, body: "the disk is full"}
		}
		return answer{status: http.StatusOK, body: `"resized"`}
	})
	e := d.engine(worker)

	id := trigger(t, e, "resize", `{}`, nil)
	deliver(t, d, e, "resize", "resize", d.receive(t, "resize", "resize", 10*time.Second))
	run := retrieve(t, e, id)
	if run.GetStatus() != taskv1.RunStatus_RUN_STATUS_QUEUED || run.GetAttempts() != 1 || !strings.Contains(run.GetError(), "the disk is full") {
		t.Fatalf("after a failed attempt the run = %v, want it queued again with the attempt's error", run)
	}
	failedAt := time.Now()
	retry := d.receive(t, "resize", "resize", 15*time.Second)
	if waited := time.Since(failedAt); waited < 500*time.Millisecond {
		t.Errorf("the next attempt was on the queue after %v, want it held for at least half the 2s backoff", waited)
	}
	deliver(t, d, e, "resize", "resize", retry)
	if run := retrieve(t, e, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED || run.GetAttempts() != 2 || run.GetError() != "" {
		t.Errorf("run = %v, want it completed on its second attempt with no error kept", run)
	}
}

func TestLiveARunWhoseEveryAttemptFailsOrAbortsEndsFailedWithoutAnotherMessage(t *testing.T) {
	em := live(t)
	abort, err := protojson.Marshal(&topicv1.Answer{Outcome: &topicv1.Answer_Abort{Abort: &topicv1.Abort{Reason: "no such image"}}})
	if err != nil {
		t.Fatal(err)
	}
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize", retrying(3, time.Second, time.Second))}, nil)
	worker := newFakeWorker(t, func(envelope *topicv1.Envelope) answer {
		if strings.Contains(string(envelope.GetPayload()), "abort") {
			return answer{status: http.StatusUnprocessableEntity, body: string(abort)}
		}
		return answer{status: http.StatusInternalServerError, body: "boom"}
	})
	e := d.engine(worker)

	aborted := trigger(t, e, "resize", `{"abort":true}`, nil)
	deliver(t, d, e, "resize", "resize", d.receive(t, "resize", "resize", 10*time.Second))
	if run := retrieve(t, e, aborted); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_FAILED || run.GetAttempts() != 1 || run.GetError() != "no such image" {
		t.Errorf("an aborted run = %v, want it failed on its one attempt with the abort's reason", run)
	}
	d.quiet(t, "resize", "resize", time.Second)

	lowered := trigger(t, e, "resize", `{}`, &taskv1.TriggerOptions{MaxAttempts: 1})
	deliver(t, d, e, "resize", "resize", d.receive(t, "resize", "resize", 10*time.Second))
	if run := retrieve(t, e, lowered); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_FAILED || run.GetAttempts() != 1 || !strings.Contains(run.GetError(), "boom") {
		t.Errorf("a run lowered to one attempt = %v, want it failed with the worker's answer", run)
	}
	d.quiet(t, "resize", "resize", time.Second)
}

func TestLiveATaskAttemptPastItsMaxDurationEndsTimedOut(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize", outliving(time.Second))}, nil)
	worker := newFakeWorker(t, func(*topicv1.Envelope) answer { return answer{status: http.StatusOK, hold: 5 * time.Second} })
	e := d.engine(worker)

	id := trigger(t, e, "resize", `{}`, nil)
	deliver(t, d, e, "resize", "resize", d.receive(t, "resize", "resize", 10*time.Second))
	if run := retrieve(t, e, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_TIMED_OUT || run.GetAttempts() != 1 {
		t.Errorf("run = %v, want it timed out after one attempt", run)
	}
}

func TestLiveADelayedRunWaitsOffTheQueueAndAnEarlyMessageWaitsAgain(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize")}, nil)
	worker := newFakeWorker(t, succeeding)
	e := d.engine(worker)

	id := trigger(t, e, "resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(3 * time.Second))})
	if run := retrieve(t, e, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_DELAYED {
		t.Fatalf("run = %v, want it delayed", run)
	}
	d.quiet(t, "resize", "resize", time.Second)
	deliver(t, d, e, "resize", "resize", d.receive(t, "resize", "resize", 10*time.Second))
	run := retrieve(t, e, id)
	if run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED || run.GetStartedAt().AsTime().Before(run.GetDueAt().AsTime()) {
		t.Errorf("run = %v, want it completed, started no earlier than it was due", run)
	}

	later := trigger(t, e, "resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	if _, err := e.Tasks().RescheduleRun(context.Background(), &taskv1.RescheduleRunRequest{Id: later, DueAt: timestamppb.New(time.Now().Add(2 * time.Second))}); err != nil {
		t.Fatalf("RescheduleRun: %v", err)
	}
	deliver(t, d, e, "resize", "resize", d.receive(t, "resize", "resize", 10*time.Second))
	if run := retrieve(t, e, later); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED {
		t.Errorf("a run rescheduled sooner = %v, want it completed at its new time", run)
	}
	if _, err := e.Tasks().RescheduleRun(context.Background(), &taskv1.RescheduleRunRequest{Id: later, DueAt: timestamppb.New(time.Now().Add(time.Hour))}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("rescheduling an ended run = %v, want FailedPrecondition", err)
	}
}

func TestLiveAnOrderedTaskIsSentWithoutAPerMessageDelayItsFIFOQueueWouldRefuse(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"sequence": aTask("sequence", ordered)}, nil)
	worker := newFakeWorker(t, succeeding)
	e := d.engine(worker)

	id := trigger(t, e, "sequence", `{}`, &taskv1.TriggerOptions{Key: "k", DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	early := d.receive(t, "sequence", "sequence", 5*time.Second)
	if failed := deliver(t, d, e, "sequence", "sequence", early); len(failed) != 1 {
		t.Fatalf("an early message on a FIFO queue was acknowledged, want it held on the queue until due")
	}
	if run := retrieve(t, e, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_DELAYED || run.GetAttempts() != 0 {
		t.Errorf("run = %v, want it still delayed", run)
	}
}

func TestLiveARepeatedIdempotencyKeyReturnsTheFirstRun(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize")}, nil)
	e := d.engine(nil)

	first := trigger(t, e, "resize", `{"n":1}`, &taskv1.TriggerOptions{IdempotencyKey: "order-1"})
	if again := trigger(t, e, "resize", `{"n":2}`, &taskv1.TriggerOptions{IdempotencyKey: "order-1"}); again != first {
		t.Errorf("a repeated key started %s, want the first run %s", again, first)
	}
	expiring := trigger(t, e, "resize", `{}`, &taskv1.TriggerOptions{IdempotencyKey: "order-2", IdempotencyKeyTtl: durationpb.New(time.Second)})
	time.Sleep(1500 * time.Millisecond)
	if again := trigger(t, e, "resize", `{}`, &taskv1.TriggerOptions{IdempotencyKey: "order-2", IdempotencyKeyTtl: durationpb.New(time.Second)}); again == expiring {
		t.Error("a key past its ttl returned the old run")
	}
}

func TestLiveTriggersSharingADebounceKeyFoldIntoTheFirstRunDueAfterTheLast(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize")}, nil)
	e := d.engine(nil)

	debounce := &taskv1.Debounce{Key: "k", Delay: durationpb.New(2 * time.Second)}
	first := trigger(t, e, "resize", `{"n":1}`, &taskv1.TriggerOptions{Debounce: debounce})
	time.Sleep(500 * time.Millisecond)
	second := trigger(t, e, "resize", `{"n":2}`, &taskv1.TriggerOptions{Debounce: debounce})
	if second != first {
		t.Fatalf("the second trigger started %s, want it folded into %s", second, first)
	}
	run := retrieve(t, e, first)
	if folded := run.GetDueAt().AsTime().Sub(run.GetCreatedAt().AsTime()); folded < 2400*time.Millisecond {
		t.Errorf("the run is due %v after it was created, want the delay after the last trigger", folded)
	}
	if string(run.GetPayload()) != `{"n":1}` {
		t.Errorf("payload = %s, want the first trigger's", run.GetPayload())
	}
}

func TestLiveACanceledRunIsNeverAttemptedAndAnExecutingOneIsAborted(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize")}, nil)
	worker := newFakeWorker(t, func(*topicv1.Envelope) answer { return answer{status: http.StatusOK, hold: 20 * time.Second} })
	e := d.engine(worker)

	queued := trigger(t, e, "resize", `{}`, nil)
	if resp, err := e.Tasks().CancelRun(context.Background(), &taskv1.CancelRunRequest{Id: queued}); err != nil || resp.GetRun().GetStatus() != taskv1.RunStatus_RUN_STATUS_CANCELED {
		t.Fatalf("CancelRun = %v, %v, want it canceled", resp, err)
	}
	deliver(t, d, e, "resize", "resize", d.receive(t, "resize", "resize", 10*time.Second))
	if run := retrieve(t, e, queued); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_CANCELED || run.GetAttempts() != 0 || len(worker.deliveries()) != 0 {
		t.Fatalf("run = %v with %d deliveries, want it canceled and never attempted", run, len(worker.deliveries()))
	}

	executing := trigger(t, e, "resize", `{}`, nil)
	record := d.receive(t, "resize", "resize", 10*time.Second)
	go func() {
		for retrieve(t, e, executing).GetStatus() != taskv1.RunStatus_RUN_STATUS_EXECUTING {
			time.Sleep(100 * time.Millisecond)
		}
		_, _ = e.Tasks().CancelRun(context.Background(), &taskv1.CancelRunRequest{Id: executing})
	}()
	began := time.Now()
	deliver(t, d, e, "resize", "resize", record)
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("the canceled attempt ran for %v, want it aborted within a second or two of the cancel", took)
	}
	if run := retrieve(t, e, executing); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_CANCELED || run.GetAttempts() != 1 {
		t.Errorf("run = %v, want it canceled after its one attempt", run)
	}
	d.quiet(t, "resize", "resize", time.Second)
}

func TestLiveARunThatWaitsPastItsTTLEndsExpiredWithoutAnAttempt(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize")}, nil)
	worker := newFakeWorker(t, succeeding)
	e := d.engine(worker)

	id := trigger(t, e, "resize", `{}`, &taskv1.TriggerOptions{Ttl: durationpb.New(time.Second)})
	record := d.receive(t, "resize", "resize", 10*time.Second)
	time.Sleep(1500 * time.Millisecond)
	deliver(t, d, e, "resize", "resize", record)
	if run := retrieve(t, e, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_EXPIRED || run.GetAttempts() != 0 {
		t.Errorf("run = %v, want it expired with no attempt", run)
	}
}

func TestLiveRunsAreListedNewestFirstByTaskStatusAndTags(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"echo": aTask("echo"), "flaky": aTask("flaky")}, nil)
	e := d.engine(nil)

	older := trigger(t, e, "echo", `{}`, &taskv1.TriggerOptions{Tags: []string{"listed"}})
	newer := trigger(t, e, "echo", `{}`, &taskv1.TriggerOptions{Tags: []string{"listed", "second"}})
	delayed := trigger(t, e, "flaky", `{}`, &taskv1.TriggerOptions{Tags: []string{"listed"}, DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	list := func(req *taskv1.ListRunsRequest) []string {
		resp, err := e.Tasks().ListRuns(context.Background(), req)
		if err != nil {
			t.Fatalf("ListRuns(%v): %v", req, err)
		}
		var ids []string
		for _, run := range resp.GetRuns() {
			ids = append(ids, run.GetId())
		}
		return ids
	}
	for _, tc := range []struct {
		req  *taskv1.ListRunsRequest
		want []string
	}{
		{&taskv1.ListRunsRequest{Tags: []string{"listed"}}, []string{delayed, newer, older}},
		{&taskv1.ListRunsRequest{Tags: []string{"listed", "second"}}, []string{newer}},
		{&taskv1.ListRunsRequest{Task: "echo", Tags: []string{"listed"}}, []string{newer, older}},
		{&taskv1.ListRunsRequest{Statuses: []taskv1.RunStatus{taskv1.RunStatus_RUN_STATUS_DELAYED}}, []string{delayed}},
	} {
		if got := list(tc.req); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("ListRuns(%v) = %v, want %v", tc.req, got, tc.want)
		}
	}
	page, err := e.Tasks().ListRuns(context.Background(), &taskv1.ListRunsRequest{Tags: []string{"listed"}, Limit: 2})
	if err != nil || len(page.GetRuns()) != 2 || page.GetNextCursor() == "" {
		t.Fatalf("a first page of 2 = %v, %v, want two runs and a cursor", page, err)
	}
	if rest := list(&taskv1.ListRunsRequest{Tags: []string{"listed"}, Cursor: page.GetNextCursor()}); strings.Join(rest, ",") != older {
		t.Errorf("the page after the cursor = %v, want [%s]", rest, older)
	}
}

func TestLiveAWorkerAtItsConcurrencyPutsAnotherRunBackOnTheQueue(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize")}, map[string]queues.Worker{"worker": {Concurrency: 1}})
	release := make(chan struct{})
	worker := newFakeWorker(t, func(envelope *topicv1.Envelope) answer {
		if strings.Contains(string(envelope.GetPayload()), "first") {
			<-release
		}
		return answer{status: http.StatusOK, body: `{}`}
	})
	e := d.engine(worker)

	first := trigger(t, e, "resize", `{"first":true}`, nil)
	firstRecord := d.receive(t, "resize", "resize", 10*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		deliver(t, d, e, "resize", "resize", firstRecord)
	}()
	for retrieve(t, e, first).GetStatus() != taskv1.RunStatus_RUN_STATUS_EXECUTING {
		time.Sleep(50 * time.Millisecond)
	}
	second := trigger(t, e, "resize", `{}`, nil)
	deliver(t, d, e, "resize", "resize", d.receive(t, "resize", "resize", 10*time.Second))
	if run := retrieve(t, e, second); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_QUEUED || run.GetAttempts() != 0 {
		t.Errorf("a run delivered while the worker was full = %v, want it still queued", run)
	}
	close(release)
	<-done
	deliver(t, d, e, "resize", "resize", d.receive(t, "resize", "resize", 10*time.Second))
	if run := retrieve(t, e, second); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED {
		t.Errorf("the run put back = %v, want it completed once the worker had room", run)
	}
}

func TestLiveACronFiringTriggersOneRunPerScheduledMinute(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"heartbeat": aTask("heartbeat")}, nil)
	e := d.engine(nil)

	scheduled := time.Date(2026, 10, 2, 12, 34, 7, 0, time.UTC)
	for range 2 {
		if err := e.FireCron(context.Background(), "heartbeat", scheduled); err != nil {
			t.Fatalf("FireCron: %v", err)
		}
	}
	resp, err := e.Tasks().ListRuns(context.Background(), &taskv1.ListRunsRequest{Task: "heartbeat"})
	if err != nil || len(resp.GetRuns()) != 1 {
		t.Fatalf("ListRuns = %v, %v, want the one run both firings of a minute share", resp, err)
	}
	var payload map[string]string
	if err := json.Unmarshal(resp.GetRuns()[0].GetPayload(), &payload); err != nil || payload["timestamp"] != "2026-10-02T12:34:00Z" {
		t.Errorf("payload = %s, want the scheduled minute's timestamp", resp.GetRuns()[0].GetPayload())
	}
}

func TestLiveEveryRunExpiresFromTheTableEvenIfItNeverFinishes(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"resize": aTask("resize")}, nil)
	e := d.engine(nil)

	due := time.Now().Add(time.Hour)
	id := trigger(t, e, "resize", `{}`, &taskv1.TriggerOptions{DueAt: timestamppb.New(due)})
	item, found, err := e.store.readRunItem(context.Background(), "resize", id, true)
	if err != nil || !found {
		t.Fatalf("readRunItem = %v, %v", found, err)
	}
	if until := time.Unix(item.ExpiresAtUnix, 0); until.Before(due.Add(runRetention - time.Minute)) {
		t.Errorf("a delayed run expires from the table at %v, want no sooner than %v after it is due, and never left without an expiry", until, runRetention)
	}
	moved := time.Now().Add(2 * time.Hour)
	if _, err := e.Tasks().RescheduleRun(context.Background(), &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.New(moved)}); err != nil {
		t.Fatalf("RescheduleRun: %v", err)
	}
	item, _, _ = e.store.readRunItem(context.Background(), "resize", id, true)
	if until := time.Unix(item.ExpiresAtUnix, 0); until.Before(moved.Add(runRetention - time.Minute)) {
		t.Errorf("a rescheduled run expires at %v, want its expiry moved with its due time", until)
	}
}

func TestLiveAnOrderedRunStillWaitingForASlotOnItsMessagesLastDeliveryEndsFailedNotStrandedInTheDeadLetterQueue(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"sequence": aTask("sequence", ordered)}, map[string]queues.Worker{"worker": {Concurrency: 1}})
	e := d.engine(newFakeWorker(t, func(*topicv1.Envelope) answer { return answer{status: http.StatusOK, body: `{}`} }))
	if _, taken, err := e.store.takeSlot(context.Background(), slotSet{name: "worker#worker", limit: 1}, "another-run", time.Now().Add(time.Minute)); err != nil || !taken {
		t.Fatalf("hold the worker's only slot: %v, %v", taken, err)
	}

	id := trigger(t, e, "sequence", `{}`, &taskv1.TriggerOptions{Key: "k"})
	record := d.receive(t, "sequence", "sequence", 10*time.Second)
	record.Attributes = map[string]string{"ApproximateReceiveCount": "1"}
	if retained := deliver(t, d, e, "sequence", "sequence", record); len(retained) != 1 {
		t.Fatalf("a first delivery while the worker is full retained %v, want its message held in place", retained)
	}
	record.Attributes["ApproximateReceiveCount"] = strconv.Itoa(queues.MaxReceiveCount)
	if retained := deliver(t, d, e, "sequence", "sequence", record); len(retained) != 0 {
		t.Errorf("the last delivery before the redrive retained %v, want the message acknowledged", retained)
	}
	if run := retrieve(t, e, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_FAILED || run.GetAttempts() != 0 || run.GetError() == "" {
		t.Errorf("a run whose message ran out of deliveries waiting = %v, want it failed without an attempt, saying why", run)
	}
}

func TestLiveAnOrderedRunRescheduledSoonerRunsAtItsNewTimeThoughItsMessageWasHeldUntilTheOldOne(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"sequence": aTask("sequence", ordered)}, nil)
	worker := newFakeWorker(t, succeeding)
	e := d.engine(worker)

	id := trigger(t, e, "sequence", `{}`, &taskv1.TriggerOptions{Key: "k", DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	if retained := deliver(t, d, e, "sequence", "sequence", d.receive(t, "sequence", "sequence", 5*time.Second)); len(retained) != 1 {
		t.Fatalf("an early message on a FIFO queue retained %v, want it held on the queue until due", retained)
	}
	if _, err := e.Tasks().RescheduleRun(context.Background(), &taskv1.RescheduleRunRequest{Id: id, DueAt: timestamppb.New(time.Now())}); err != nil {
		t.Fatalf("RescheduleRun: %v", err)
	}
	deliver(t, d, e, "sequence", "sequence", d.receive(t, "sequence", "sequence", 10*time.Second))
	if run := retrieve(t, e, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED || run.GetAttempts() != 1 {
		t.Errorf("an ordered run rescheduled to now = %v, want it completed without waiting out its old due time", run)
	}
}
