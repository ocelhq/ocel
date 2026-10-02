package tasks

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"

	"github.com/aws/aws-lambda-go/events"
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
)

func aTopic(consumers ...provider.ConsumerSpec) *provider.TopicSpec {
	for i := range consumers {
		if consumers[i].Retry.MaxAttempts == 0 {
			consumers[i].Retry = provider.ResolveRetryPolicy()
		}
	}
	return &provider.TopicSpec{Consumers: consumers}
}

func send(t *testing.T, e *Engine, topic, payload string, req *topicv1.SendRequest) string {
	t.Helper()
	if req == nil {
		req = &topicv1.SendRequest{}
	}
	req.Topic, req.Payload = topic, []byte(payload)
	resp, err := e.Topics().Send(context.Background(), req)
	if err != nil {
		t.Fatalf("Send(%s): %v", topic, err)
	}
	return resp.GetMessageId()
}

func TestLiveAMessageSentToATopicReachesEachConsumerOnItsOwnQueue(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"orders": aTopic(
		provider.ConsumerSpec{Name: "audit-log", Worker: "worker"},
		provider.ConsumerSpec{Name: "ledger-entry", Worker: "ledger"},
	)}, nil)
	worker := newFakeWorker(t, succeeding)
	e := d.engine(worker)

	id := send(t, e, "orders", exactJSON, &topicv1.SendRequest{IdempotencyKey: "once"})
	if again := send(t, e, "orders", exactJSON, &topicv1.SendRequest{IdempotencyKey: "once"}); again != id {
		t.Errorf("a repeated idempotency key sent %s, want the first message %s", again, id)
	}
	for _, consumer := range []string{"audit-log", "ledger-entry"} {
		deliver(t, d, e, "orders", consumer, d.receive(t, "orders", consumer, 10*time.Second))
		d.quiet(t, "orders", consumer, time.Second)
	}
	got := worker.deliveries()
	if len(got) != 2 {
		t.Fatalf("the worker received %d envelopes, want one per consumer", len(got))
	}
	seen := map[string]bool{}
	for _, one := range got {
		envelope := one.envelope
		seen[envelope.GetConsumer()] = true
		if envelope.GetTopic() != "orders" || envelope.GetMessage().GetId() != id || envelope.GetExecution() != id+"-"+envelope.GetConsumer() {
			t.Errorf("envelope = %v, want topic orders, message %s and its consumer's execution", envelope, id)
		}
		if payload := rawField(t, one.body, "payload"); payload != exactJSON {
			t.Errorf("payload = %s, want %s unchanged", payload, exactJSON)
		}
	}
	if !seen["audit-log"] || !seen["ledger-entry"] {
		t.Errorf("consumers delivered to = %v, want both", seen)
	}
}

func TestLiveARedriveWhoseMessageIsNotSentLeavesTheDeadLetterWhereItWas(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"doomed": aTopic(provider.ConsumerSpec{
		Name: "fails-once", Worker: "worker", Retry: provider.RetryPolicy{MaxAttempts: 1},
	})}, nil)
	worker := newFakeWorker(t, func(*topicv1.Envelope) answer {
		return answer{status: http.StatusInternalServerError, body: "this consumer fails"}
	})
	e := d.engine(worker)
	ctx := context.Background()

	send(t, e, "doomed", `{}`, nil)
	deliver(t, d, e, "doomed", "fails-once", d.receive(t, "doomed", "fails-once", 10*time.Second))
	redrive := &topicv1.RedriveDeadLettersRequest{Topic: "doomed", Consumer: "fails-once"}
	if _, err := d.engineThatCannotSend().Topics().RedriveDeadLetters(ctx, redrive); err == nil {
		t.Fatal("RedriveDeadLetters succeeded though its message was never sent")
	}
	if left, err := e.Topics().ListDeadLetters(ctx, &topicv1.ListDeadLettersRequest{Topic: "doomed", Consumer: "fails-once"}); err != nil || len(left.GetDeadLetters()) != 1 {
		t.Fatalf("dead letters after an unsent redrive = %v, %v, want the one it could not send", left, err)
	}
	if redriven, err := e.Topics().RedriveDeadLetters(ctx, redrive); err != nil || redriven.GetRedriven() != 1 {
		t.Errorf("a second RedriveDeadLetters = %v, %v, want it to send the letter", redriven, err)
	}
}

func TestLiveAMessageAConsumerFailsOnEveryAttemptIsDeadLetteredAndCanBeRedrivenAndPurged(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"doomed": aTopic(provider.ConsumerSpec{
		Name: "always-fails", Worker: "worker",
		Retry: provider.RetryPolicy{MaxAttempts: 2, MinDelay: time.Second, MaxDelay: time.Second},
	})}, nil)
	worker := newFakeWorker(t, func(*topicv1.Envelope) answer {
		return answer{status: http.StatusInternalServerError, body: "this consumer fails every attempt"}
	})
	e := d.engine(worker)
	ctx := context.Background()

	id := send(t, e, "doomed", `{"probe":"doomed"}`, nil)
	for range 2 {
		deliver(t, d, e, "doomed", "always-fails", d.receive(t, "doomed", "always-fails", 10*time.Second))
	}
	d.quiet(t, "doomed", "always-fails", 2*time.Second)
	listed, err := e.Topics().ListDeadLetters(ctx, &topicv1.ListDeadLettersRequest{Topic: "doomed", Consumer: "always-fails"})
	if err != nil || len(listed.GetDeadLetters()) != 1 {
		t.Fatalf("ListDeadLetters = %v, %v, want the one failed message", listed, err)
	}
	letter := listed.GetDeadLetters()[0]
	if letter.GetMessage().GetId() != id || letter.GetAttempts() != 2 || string(letter.GetPayload()) != `{"probe":"doomed"}` || !strings.Contains(letter.GetError(), "fails every attempt") {
		t.Errorf("dead letter = %v, want message %s after 2 attempts with its payload and error", letter, id)
	}
	if count, err := e.Topics().CountDeadLetters(ctx, &topicv1.CountDeadLettersRequest{Topic: "doomed", Consumer: "always-fails"}); err != nil || count.GetCount() != 1 {
		t.Errorf("CountDeadLetters = %v, %v, want 1", count, err)
	}

	redriven, err := e.Topics().RedriveDeadLetters(ctx, &topicv1.RedriveDeadLettersRequest{Topic: "doomed", Consumer: "always-fails", Executions: []string{letter.GetExecution()}})
	if err != nil || redriven.GetRedriven() != 1 {
		t.Fatalf("RedriveDeadLetters = %v, %v, want 1", redriven, err)
	}
	if left, _ := e.Topics().ListDeadLetters(ctx, &topicv1.ListDeadLettersRequest{Topic: "doomed", Consumer: "always-fails"}); len(left.GetDeadLetters()) != 0 {
		t.Errorf("a redriven message is still listed as dead: %v", left)
	}
	for range 2 {
		deliver(t, d, e, "doomed", "always-fails", d.receive(t, "doomed", "always-fails", 10*time.Second))
	}
	again, _ := e.Topics().ListDeadLetters(ctx, &topicv1.ListDeadLettersRequest{Topic: "doomed", Consumer: "always-fails"})
	if len(again.GetDeadLetters()) != 1 || again.GetDeadLetters()[0].GetExecution() != letter.GetExecution() {
		t.Fatalf("after a redriven message failed again the dead letters = %v, want it back under its execution", again)
	}
	purged, err := e.Topics().PurgeDeadLetters(ctx, &topicv1.PurgeDeadLettersRequest{Topic: "doomed", Consumer: "always-fails", Executions: []string{letter.GetExecution()}})
	if err != nil || purged.GetPurged() != 1 {
		t.Errorf("PurgeDeadLetters = %v, %v, want 1", purged, err)
	}
}

func TestLiveABatchConsumerIsDeliveredItsMessagesInOneEnvelopeAndRetriesOnlyWhatItHeld(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"tally": aTask("tally", retrying(3, time.Second, time.Second), func(topic *provider.TopicSpec) {
		topic.Consumers[0].Batch = &provider.BatchPolicy{Size: 5, Timeout: time.Second}
	})}, nil)
	failed := false
	worker := newFakeWorker(t, func(envelope *topicv1.Envelope) answer {
		if !failed {
			failed = true
			return answer{status: http.StatusInternalServerError, body: "the first batch fails"}
		}
		return answer{status: http.StatusOK, body: `{}`}
	})
	e := d.engine(worker)

	first := trigger(t, e, "tally", `{"n":1}`, nil)
	second := trigger(t, e, "tally", `{"n":2}`, nil)
	records := append([]events.SQSMessage{d.receive(t, "tally", "tally", 10*time.Second)}, d.receive(t, "tally", "tally", 10*time.Second))
	deliver(t, d, e, "tally", "tally", records...)
	got := worker.deliveries()
	if len(got) != 1 || len(got[0].envelope.GetMessages()) != 2 {
		t.Fatalf("the worker received %d envelopes, want one holding both runs", len(got))
	}
	for _, id := range []string{first, second} {
		if run := retrieve(t, e, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_QUEUED || run.GetAttempts() != 1 {
			t.Errorf("a run the failed batch held = %v, want it queued again after its one attempt", run)
		}
	}
	retries := []events.SQSMessage{d.receive(t, "tally", "tally", 10*time.Second), d.receive(t, "tally", "tally", 10*time.Second)}
	deliver(t, d, e, "tally", "tally", retries...)
	for _, id := range []string{first, second} {
		if run := retrieve(t, e, id); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED || run.GetAttempts() != 2 {
			t.Errorf("a retried run = %v, want it completed on its second attempt", run)
		}
	}
}

func TestLiveAFailedRunOfAnOrderedTaskIsRetriedInPlaceAheadOfItsKey(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"sequence": aTask("sequence", ordered, retrying(3, time.Second, time.Second))}, nil)
	worker := newFakeWorker(t, func(envelope *topicv1.Envelope) answer {
		if strings.Contains(string(envelope.GetPayload()), "first") && envelope.GetAttempt().GetNumber() == 1 {
			return answer{status: http.StatusInternalServerError, body: "the first attempt fails"}
		}
		return answer{status: http.StatusOK, body: `{}`}
	})
	e := d.engine(worker)

	first := trigger(t, e, "sequence", `{"first":true}`, &taskv1.TriggerOptions{Key: "k"})
	trigger(t, e, "sequence", `{"second":true}`, &taskv1.TriggerOptions{Key: "k"})
	deliver(t, d, e, "sequence", "sequence", d.receive(t, "sequence", "sequence", 10*time.Second))
	if run := retrieve(t, e, first); run.GetStatus() != taskv1.RunStatus_RUN_STATUS_COMPLETED || run.GetAttempts() != 2 {
		t.Fatalf("the first run of the key = %v, want it completed on its second attempt within the one delivery", run)
	}
	deliver(t, d, e, "sequence", "sequence", d.receive(t, "sequence", "sequence", 10*time.Second))
	got := worker.deliveries()
	if len(got) != 3 || !strings.Contains(string(got[2].envelope.GetPayload()), "second") {
		t.Errorf("deliveries = %d, want the first run twice and then the second", len(got))
	}
}

func TestLiveAnOrderedBatchConsumerHoldsEveryLaterMessageOnceOneIsHeld(t *testing.T) {
	em := live(t)
	d := em.deploy(t, map[string]*provider.TopicSpec{"tally": aTask("tally", ordered, func(topic *provider.TopicSpec) {
		topic.Consumers[0].Batch = &provider.BatchPolicy{Size: 5}
	})}, nil)
	worker := newFakeWorker(t, succeeding)
	e := d.engine(worker)

	early := trigger(t, e, "tally", `{"n":1}`, &taskv1.TriggerOptions{Key: "k", DueAt: timestamppb.New(time.Now().Add(time.Hour))})
	due := trigger(t, e, "tally", `{"n":2}`, &taskv1.TriggerOptions{Key: "k"})
	records := d.receiveBatch(t, "tally", "tally", 2, 10*time.Second)
	if retained := deliver(t, d, e, "tally", "tally", records...); len(retained) != 2 {
		t.Errorf("retained %v, want both messages: the first is early and the second is behind it", retained)
	}
	if got := worker.deliveries(); len(got) != 0 {
		t.Errorf("the worker received %d envelopes, want none while the first run of the key waits", len(got))
	}
	for _, id := range []string{early, due} {
		if run := retrieve(t, e, id); run.GetAttempts() != 0 {
			t.Errorf("run %v was attempted, want neither run of the key attempted", run)
		}
	}
}
