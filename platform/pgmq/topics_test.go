package pgmq

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func aTopic(consumers ...*contractv1.ManifestConsumer) *contractv1.ManifestTopic {
	return &contractv1.ManifestTopic{Consumers: consumers}
}

func send(t *testing.T, engine *Engine, topic, payload string, req *topicv1.SendRequest) string {
	t.Helper()
	if req == nil {
		req = &topicv1.SendRequest{}
	}
	req.Topic, req.Payload = topic, []byte(payload)
	resp, err := engine.Topics().Send(context.Background(), req)
	if err != nil {
		t.Fatalf("Send(%s): %v", topic, err)
	}
	return resp.GetMessageId()
}

func awaitDelivered(t *testing.T, worker *aWorker, n int) []delivered {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		got := worker.received()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker received %d envelopes, want %d", len(got), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestTheEngineServesTheTopicAndTaskRPCs(t *testing.T) {
	engine := anEngine(t)
	topicv1connect.NewTopicServiceHandler(engine.Topics())
	taskv1connect.NewTaskServiceHandler(engine.Tasks())
}

func TestAMessageSentToATopicReachesEveryConsumerOnItsOwnWorker(t *testing.T) {
	emails := newWorker(t, succeeding)
	ledger := newWorker(t, succeeding)
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{
		"orders": aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "mail"}, &contractv1.ManifestConsumer{Name: "ledger", Worker: "books"}),
	}, map[string]Worker{"mail": {URL: emails.server.URL}, "books": {URL: ledger.server.URL}})

	id := send(t, engine, "orders", `{"order":42}`, nil)

	for consumer, worker := range map[string]*aWorker{"email": emails, "ledger": ledger} {
		got := awaitDelivered(t, worker, 1)
		envelope := got[0].envelope
		if envelope.GetConsumer() != consumer || envelope.GetTopic() != "orders" || envelope.GetExecution() != id+"-"+consumer || envelope.GetMessage().GetId() != id {
			t.Errorf("%s received %v, want message %s as execution %s-%s", consumer, envelope, id, id, consumer)
		}
		if fieldOf(envelope.GetPayload(), "order") != 42.0 {
			t.Errorf("%s received payload %s, want the sent one", consumer, envelope.GetPayload())
		}
	}
}

func TestAnEnvelopeNamesTheDeclaredTopicAndConsumerRatherThanTheQueueItWasReadFrom(t *testing.T) {
	worker := newWorker(t, succeeding)
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{
		"order-events": aTopic(&contractv1.ManifestConsumer{Name: "send-email", Worker: "worker"}),
		"resize-image": aTask(named("resize-image")),
	}, map[string]Worker{"worker": {URL: worker.server.URL}})

	send(t, engine, "order-events", `{}`, nil)
	trigger(t, engine, "resize-image", `{}`, nil)

	names := map[string]string{}
	for _, got := range awaitDelivered(t, worker, 2) {
		names[got.envelope.GetConsumer()] = got.envelope.GetTopic()
	}
	if want := map[string]string{"send-email": "order-events", "resize-image": "resize-image"}; !maps.Equal(names, want) {
		t.Errorf("envelopes named consumer → topic %v, want %v, the names declared and not those of the queues %s and %s",
			names, want, queueName("order-events", "send-email"), queueName("resize-image", "resize-image"))
	}
}

func TestASendWithAKnownIdempotencyKeyReturnsTheFirstMessage(t *testing.T) {
	worker := newWorker(t, succeeding)
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"orders": aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "worker"})},
		map[string]Worker{"worker": {URL: worker.server.URL}})

	first := send(t, engine, "orders", `{}`, &topicv1.SendRequest{IdempotencyKey: "order-42"})
	second := send(t, engine, "orders", `{}`, &topicv1.SendRequest{IdempotencyKey: "order-42"})

	if second != first {
		t.Errorf("Send with a known key = %s, want the first message %s", second, first)
	}
	awaitDelivered(t, worker, 1)
	time.Sleep(300 * time.Millisecond)
	if got := len(worker.received()); got != 1 {
		t.Errorf("the worker received %d messages, want 1", got)
	}
}

func TestSendingToATaskOrToNoTopicIsRefused(t *testing.T) {
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"resize": aTask()}, nil)
	for _, topic := range []string{"resize", "nothing"} {
		_, err := engine.Topics().Send(context.Background(), &topicv1.SendRequest{Topic: topic, Payload: []byte(`{}`)})
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("Send(%s) = %v, want NotFound", topic, err)
		}
	}
}

func deadLettered(t *testing.T) (*Engine, *atomic.Bool, []string) {
	t.Helper()
	healed := &atomic.Bool{}
	worker := newWorker(t, func(*topicv1.Envelope) reply {
		if healed.Load() {
			return reply{status: http.StatusOK}
		}
		return reply{status: http.StatusBadGateway, body: "the mail server is down"}
	})
	orders := aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "worker"})
	orders.Retry = &resourcesv1.RetryPolicy{MaxAttempts: 1}
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"orders": orders}, map[string]Worker{"worker": {URL: worker.server.URL}})
	var sent []string
	for _, payload := range []string{`{"n":1}`, `{"n":2}`, `{"n":3}`} {
		sent = append(sent, send(t, engine, "orders", payload, nil)+"-email")
	}
	deadline := time.Now().Add(15 * time.Second)
	for countDeadLetters(t, engine) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("%d messages dead-lettered, want 3", countDeadLetters(t, engine))
		}
		time.Sleep(50 * time.Millisecond)
	}
	return engine, healed, sent
}

func countDeadLetters(t *testing.T, engine *Engine) int64 {
	t.Helper()
	resp, err := engine.Topics().CountDeadLetters(context.Background(), &topicv1.CountDeadLettersRequest{Topic: "orders", Consumer: "email"})
	if err != nil {
		t.Fatal(err)
	}
	return resp.GetCount()
}

func TestAListOfDeadLettersHoldsAtMostAThousandAPage(t *testing.T) {
	engine := applied(t, map[string]*contractv1.ManifestTopic{"orders": aTopic(&contractv1.ManifestConsumer{Name: "email", Worker: "mail"})}, nil)
	if _, err := engine.pool.Exec(context.Background(), `
		INSERT INTO ocel.runs (execution, topic, consumer, status, created_at, finished_at, revision, message_id, published_at)
		SELECT 'e' || n, 'orders', 'email', 'failed', now(), now(), 'r', 'm' || n, now() FROM generate_series(1, 1001) AS n`); err != nil {
		t.Fatal(err)
	}

	resp, err := engine.Topics().ListDeadLetters(context.Background(), &topicv1.ListDeadLettersRequest{Topic: "orders", Consumer: "email", Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetDeadLetters()) != 1000 || resp.GetNextCursor() == "" {
		t.Errorf("ListDeadLetters(limit 5000) = %d with cursor %q, want a page of 1000 and a cursor", len(resp.GetDeadLetters()), resp.GetNextCursor())
	}
}

func TestAConsumersAttemptPastMaxDurationIsRetriedAndThenDeadLettered(t *testing.T) {
	worker := newWorker(t, func(*topicv1.Envelope) reply { return reply{status: http.StatusOK, hold: 5 * time.Second} })
	email := &contractv1.ManifestConsumer{Name: "email", Worker: "worker", MaxDuration: durationpb.New(300 * time.Millisecond)}
	orders := aTopic(email)
	orders.Retry = &resourcesv1.RetryPolicy{MaxAttempts: 2, MinDelay: durationpb.New(50 * time.Millisecond), MaxDelay: durationpb.New(50 * time.Millisecond)}
	engine := dispatching(t, map[string]*contractv1.ManifestTopic{"orders": orders}, map[string]Worker{"worker": {URL: worker.server.URL}})

	send(t, engine, "orders", `{"n":1}`, nil)
	deadline := time.Now().Add(15 * time.Second)
	for countDeadLetters(t, engine) < 1 {
		if time.Now().After(deadline) {
			t.Fatalf("%d messages dead-lettered after %d attempts, want the timed-out message", countDeadLetters(t, engine), len(worker.received()))
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, err := engine.Topics().ListDeadLetters(context.Background(), &topicv1.ListDeadLettersRequest{Topic: "orders", Consumer: "email"})
	if err != nil {
		t.Fatal(err)
	}
	if letter := resp.GetDeadLetters()[0]; letter.GetAttempts() != 2 || !strings.Contains(letter.GetError(), "maxDuration") {
		t.Errorf("dead letter = %v, want both attempts spent and the maxDuration error", letter)
	}
	if got := len(worker.received()); got != 2 {
		t.Errorf("the worker received %d attempts, want 2", got)
	}
}

func TestAMessageThatFailsEveryAttemptIsDeadLetteredWithItsPayloadAndError(t *testing.T) {
	engine, _, sent := deadLettered(t)

	resp, err := engine.Topics().ListDeadLetters(context.Background(), &topicv1.ListDeadLettersRequest{Topic: "orders", Consumer: "email", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.GetDeadLetters()) != 2 || resp.GetNextCursor() == "" {
		t.Fatalf("ListDeadLetters(limit 2) = %d with cursor %q, want a full page and a cursor", len(resp.GetDeadLetters()), resp.GetNextCursor())
	}
	rest, err := engine.Topics().ListDeadLetters(context.Background(), &topicv1.ListDeadLettersRequest{Topic: "orders", Consumer: "email", Cursor: resp.GetNextCursor()})
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, letter := range append(resp.GetDeadLetters(), rest.GetDeadLetters()...) {
		listed = append(listed, letter.GetExecution())
		if fieldOf(letter.GetPayload(), "n") == nil || letter.GetAttempts() != 1 || letter.GetError() == "" || letter.GetFailedAt() == nil || letter.GetMessage().GetId() == "" {
			t.Errorf("dead letter = %v, want its payload, attempts, error, message and when it failed", letter)
		}
	}
	slices.Sort(listed)
	if !slices.Equal(listed, sent) {
		t.Errorf("dead letters = %v, want %v", listed, sent)
	}
}

func TestRedrivenDeadLettersAreDeliveredAgain(t *testing.T) {
	engine, healed, sent := deadLettered(t)
	healed.Store(true)

	resp, err := engine.Topics().RedriveDeadLetters(context.Background(), &topicv1.RedriveDeadLettersRequest{Topic: "orders", Consumer: "email", Executions: sent[:1]})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetRedriven() != 1 {
		t.Errorf("redriven = %d, want 1", resp.GetRedriven())
	}
	deadline := time.Now().Add(10 * time.Second)
	for countDeadLetters(t, engine) != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("%d dead letters after redriving one, want 2", countDeadLetters(t, engine))
		}
		time.Sleep(50 * time.Millisecond)
	}
	run, err := engine.Store().ReadRun(context.Background(), sent[0])
	if err != nil {
		t.Fatal(err)
	}
	for run.Status != "completed" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		run, _ = engine.Store().ReadRun(context.Background(), sent[0])
	}
	if run.Status != "completed" {
		t.Errorf("the redriven execution is %s, want completed", run.Status)
	}

	all, err := engine.Topics().RedriveDeadLetters(context.Background(), &topicv1.RedriveDeadLettersRequest{Topic: "orders", Consumer: "email"})
	if err != nil {
		t.Fatal(err)
	}
	if all.GetRedriven() != 2 {
		t.Errorf("redriving every dead letter = %d, want the 2 left", all.GetRedriven())
	}
}

func TestPurgedDeadLettersAreGone(t *testing.T) {
	engine, _, sent := deadLettered(t)

	one, err := engine.Topics().PurgeDeadLetters(context.Background(), &topicv1.PurgeDeadLettersRequest{Topic: "orders", Consumer: "email", Executions: sent[:1]})
	if err != nil || one.GetPurged() != 1 {
		t.Fatalf("PurgeDeadLetters(one) = %v, %v, want 1 purged", one, err)
	}
	if got := countDeadLetters(t, engine); got != 2 {
		t.Errorf("count after purging one = %d, want 2", got)
	}
	rest, err := engine.Topics().PurgeDeadLetters(context.Background(), &topicv1.PurgeDeadLettersRequest{Topic: "orders", Consumer: "email"})
	if err != nil || rest.GetPurged() != 2 {
		t.Fatalf("PurgeDeadLetters(all) = %v, %v, want 2 purged", rest, err)
	}
	if got := countDeadLetters(t, engine); got != 0 {
		t.Errorf("count after purging all = %d, want 0", got)
	}
}
