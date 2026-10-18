package tasks

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"google.golang.org/protobuf/types/known/timestamppb"

	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/pkg/runs/fake"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

type lambda struct {
	t      *testing.T
	engine *Engine
	sqs    *fakeSQS
	sns    *fakeSNS
	store  runs.Store
}

func newLambda(t *testing.T, workerURL string, topics map[string]queues.Topic) *lambda {
	t.Helper()
	l := &lambda{t: t, sqs: newFakeSQS(), sns: &fakeSNS{}, store: fake.NewStore()}
	l.engine = newEngine(Config{Topology: queues.Topology{Topics: topics}, Queues: l.sqs, Topics: l.sns, Worker: "media", WorkerURL: workerURL}, l.store, nil)
	return l
}

func (l *lambda) trigger(task string, options *taskv1.TriggerOptions) (string, events.SQSMessage) {
	l.t.Helper()
	resp, err := l.engine.Tasks().Trigger(context.Background(), &taskv1.TriggerRequest{Task: task, Payload: []byte(`{"n":1}`), Options: options})
	if err != nil {
		l.t.Fatalf("Trigger: %v", err)
	}
	sent := l.sqs.sentMessages()
	return resp.GetId(), recordOf("q-"+task, resp.GetId(), aws.ToString(sent[len(sent)-1].MessageBody), 1)
}

func (l *lambda) send(req *topicv1.SendRequest, queue string, receives int) (string, events.SQSMessage) {
	l.t.Helper()
	resp, err := l.engine.Topics().Send(context.Background(), req)
	if err != nil {
		l.t.Fatalf("Send: %v", err)
	}
	published := l.sns.published[len(l.sns.published)-1]
	return resp.GetMessageId(), recordOf(queue, resp.GetMessageId(), aws.ToString(published.Message), receives)
}

func (l *lambda) run(topic, execution string) runs.Run {
	l.t.Helper()
	run, err := l.store.ReadRun(context.Background(), topic, execution)
	if err != nil {
		l.t.Fatalf("ReadRun %s: %v", execution, err)
	}
	return run
}

func kept(resp events.SQSEventResponse) map[string]bool {
	ids := map[string]bool{}
	for _, failure := range resp.BatchItemFailures {
		ids[failure.ItemIdentifier] = true
	}
	return ids
}

func TestARecordWhoseRunCompletesIsAcknowledgedWithItsVisibilityLeftAlone(t *testing.T) {
	t.Parallel()

	l := newLambda(t, answering(t, answerAlways(http.StatusOK)), map[string]queues.Topic{"resize": aTask("resize", retrying(3), false)})
	id, record := l.trigger("resize", nil)

	resp := l.engine.Deliver(context.Background(), events.SQSEvent{Records: []events.SQSMessage{record}})

	if len(resp.BatchItemFailures) != 0 || len(l.sqs.hidden()) != 0 {
		t.Errorf("Deliver kept %v and hid %v, want the record acknowledged and left for Lambda to delete", resp.BatchItemFailures, l.sqs.hidden())
	}
	if run := l.run("resize", id); run.Status != provider.RunCompleted || run.Attempts != 1 {
		t.Errorf("the run is %s after %d attempts, want completed after 1", run.Status, run.Attempts)
	}
}

func TestAFailedAttemptWithAttemptsLeftIsKeptOnItsQueueAndHiddenForItsBackoff(t *testing.T) {
	t.Parallel()

	l := newLambda(t, answering(t, answerAlways(http.StatusInternalServerError)), map[string]queues.Topic{"resize": aTask("resize", retrying(3), false)})
	id, record := l.trigger("resize", nil)

	resp := l.engine.Deliver(context.Background(), events.SQSEvent{Records: []events.SQSMessage{record}})

	if !kept(resp)[record.MessageId] {
		t.Fatalf("Deliver kept %v, want the record kept on its queue for the next attempt", resp.BatchItemFailures)
	}
	if hidden, found := l.sqs.hidden()[record.ReceiptHandle]; !found || hidden < 2 || hidden > 4 {
		t.Errorf("the record was hidden for %ds (changed: %v), want the 2s to 4s backoff of a 4s retry delay", hidden, found)
	}
	if len(l.sqs.sentMessages()) != 1 {
		t.Errorf("%d messages were sent, want only the trigger's: a retry hides the message it has rather than sending another", len(l.sqs.sentMessages()))
	}
	if run := l.run("resize", id); run.Status != provider.RunQueued || run.Attempts != 1 {
		t.Errorf("the run is %s after %d attempts, want queued after 1", run.Status, run.Attempts)
	}
}

func TestARecordFromAQueueThisDeploymentDoesNotConsumeIsKept(t *testing.T) {
	t.Parallel()

	l := newLambda(t, answering(t, answerAlways(http.StatusOK)), map[string]queues.Topic{"resize": aTask("resize", retrying(3), false)})
	record := recordOf("elsewhere", "m1", `{}`, 1)

	resp := l.engine.Deliver(context.Background(), events.SQSEvent{Records: []events.SQSMessage{record}})

	if !kept(resp)["m1"] {
		t.Errorf("Deliver kept %v, want the record from a queue no consumer reads kept on it", resp.BatchItemFailures)
	}
}

func TestACronMarkerStartsItsTaskOnceForItsMinuteHoweverOftenItArrives(t *testing.T) {
	t.Parallel()

	nightly := aTask("nightly", retrying(3), false)
	nightly.Declared.Cron = "0 3 * * *"
	l := newLambda(t, answering(t, answerAlways(http.StatusOK)), map[string]queues.Topic{"nightly": nightly})
	marker := `{"ocelCron":"nightly","scheduledTime":"2026-10-12T03:00:41Z"}`

	resp := l.engine.Deliver(context.Background(), events.SQSEvent{Records: []events.SQSMessage{recordOf("q-nightly", "c1", marker, 1), recordOf("q-nightly", "c2", marker, 1)}})

	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("Deliver kept %v, want each marker acknowledged once its task is triggered", resp.BatchItemFailures)
	}
	sent := l.sqs.sentMessages()
	if len(sent) != 1 {
		t.Fatalf("%d runs were sent, want the one run of nightly for 03:00", len(sent))
	}
	msg, err := messageOf("nightly", "nightly", aws.ToString(sent[0].MessageBody))
	if err != nil {
		t.Fatal(err)
	}
	run := l.run("nightly", msg.Execution)
	if string(run.Payload) != string(runs.CronPayload(time.Date(2026, 10, 12, 3, 0, 0, 0, time.UTC))) {
		t.Errorf("the run carries %s, want the scheduled minute's payload", run.Payload)
	}
}

func TestAnAttemptIsCutOffBeforeTheInvocationsDeadlineSoItsRunIsQueuedAgainWhileAttemptsAreLeft(t *testing.T) {
	t.Parallel()

	blocking := answering(t, func(r *http.Request) int {
		<-r.Context().Done()
		return http.StatusServiceUnavailable
	})
	for maxAttempts, want := range map[int]provider.RunStatus{2: provider.RunQueued, 1: provider.RunFailed} {
		l := newLambda(t, blocking, map[string]queues.Topic{"export": aTask("export", retrying(maxAttempts), false)})
		id, record := l.trigger("export", nil)
		ctx, cancel := context.WithTimeout(context.Background(), settleMargin+time.Second)

		started := time.Now()
		resp := l.engine.Deliver(ctx, events.SQSEvent{Records: []events.SQSMessage{record}})
		took := time.Since(started)
		cancel()

		if took > settleMargin {
			t.Errorf("%d attempts: Deliver answered after %v, want the attempt cut off while %v of the invocation was left", maxAttempts, took, settleMargin)
		}
		run := l.run("export", id)
		if run.Status != want || run.Attempts != 1 {
			t.Errorf("%d attempts: the cut-off run is %s after %d attempts, want %s after 1", maxAttempts, run.Status, run.Attempts, want)
		}
		if retried := kept(resp)[record.MessageId]; retried != (want == provider.RunQueued) {
			t.Errorf("%d attempts: the record was kept %v, want it kept only while the run has attempts left", maxAttempts, retried)
		}
	}
}

func TestOnceAnOrderedRecordIsKeptEveryLaterRecordOfItsKeyIsKeptBehindIt(t *testing.T) {
	t.Parallel()

	l := newLambda(t, answering(t, answerAlways(http.StatusInternalServerError)), map[string]queues.Topic{"ledger": aTopic("ledger", true, "apply")})
	_, first := l.send(&topicv1.SendRequest{Topic: "ledger", Key: "account-1", Payload: []byte(`{"n":0}`)}, "q-ledger-apply.fifo", 1)
	_, second := l.send(&topicv1.SendRequest{Topic: "ledger", Key: "account-1", Payload: []byte(`{"n":1}`)}, "q-ledger-apply.fifo", 1)

	resp := l.engine.Deliver(context.Background(), events.SQSEvent{Records: []events.SQSMessage{first, second}})

	if held := kept(resp); !held[first.MessageId] || !held[second.MessageId] {
		t.Fatalf("Deliver kept %v, want both records of the key kept", resp.BatchItemFailures)
	}
	hidden := l.sqs.hidden()
	if hidden[first.ReceiptHandle] < 2 || hidden[second.ReceiptHandle] != 0 {
		t.Errorf("the records were hidden for %ds and %ds, want the failed one hidden for its backoff and the one behind it visible, where its FIFO group holds it", hidden[first.ReceiptHandle], hidden[second.ReceiptHandle])
	}
}

func TestAMessageDueInDaysIsHiddenNoLongerThanSQSAllowsFromItsReceipt(t *testing.T) {
	t.Parallel()

	l := newLambda(t, answering(t, answerAlways(http.StatusOK)), map[string]queues.Topic{"orders": aTopic("orders", false, "ship")})
	_, record := l.send(&topicv1.SendRequest{Topic: "orders", DueAt: timestamppb.New(time.Now().Add(72 * time.Hour)), Payload: []byte(`{}`)}, "q-orders-ship", 1)

	resp := l.engine.Deliver(context.Background(), events.SQSEvent{Records: []events.SQSMessage{record}})

	if !kept(resp)[record.MessageId] {
		t.Fatalf("Deliver kept %v, want the early record kept until it is due", resp.BatchItemFailures)
	}
	if hidden := l.sqs.hidden()[record.ReceiptHandle]; hidden < int32((maxVisibility-longestBatchingWait-time.Minute)/time.Second) || hidden > int32((maxVisibility-longestBatchingWait)/time.Second) {
		t.Errorf("the record was hidden for %ds, want the most SQS allows from a receive up to %v before the invocation", hidden, longestBatchingWait)
	}
}

func TestAMessageSQSWouldDeadLetterNextFailsItsRunInsteadOfWaitingAgain(t *testing.T) {
	t.Parallel()

	l := newLambda(t, answering(t, answerAlways(http.StatusOK)), map[string]queues.Topic{"orders": aTopic("orders", false, "ship")})
	id, record := l.send(&topicv1.SendRequest{Topic: "orders", DueAt: timestamppb.New(time.Now().Add(time.Hour)), Payload: []byte(`{"n":3}`)}, "q-orders-ship", queues.MaxReceiveCount)

	resp := l.engine.Deliver(context.Background(), events.SQSEvent{Records: []events.SQSMessage{record}})

	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("Deliver kept %v, want the record acknowledged rather than dead-lettered by SQS", resp.BatchItemFailures)
	}
	run := l.run("orders", runs.ExecutionOf(id, "ship"))
	if run.Status != provider.RunFailed || string(run.Payload) != `{"n":3}` {
		t.Errorf("the run is %s carrying %s, want it failed with its payload so it is listed as a dead letter", run.Status, run.Payload)
	}
}

func TestAnEarlyRecordThatWouldOutliveItsQueuesRetentionIsSentAgainAndAcknowledged(t *testing.T) {
	t.Parallel()

	l := newLambda(t, answering(t, answerAlways(http.StatusOK)), map[string]queues.Topic{"orders": aTopic("orders", false, "ship")})
	_, record := l.send(&topicv1.SendRequest{Topic: "orders", DueAt: timestamppb.New(time.Now().Add(72 * time.Hour)), Payload: []byte(`{}`)}, "q-orders-ship", 20)
	record.Attributes["SentTimestamp"] = strconv.FormatInt(time.Now().Add(-queues.MessageRetention+time.Hour).UnixMilli(), 10)

	resp := l.engine.Deliver(context.Background(), events.SQSEvent{Records: []events.SQSMessage{record}})

	sent := l.sqs.sentMessages()
	if len(sent) != 1 || aws.ToString(sent[0].MessageBody) != record.Body || aws.ToString(sent[0].QueueUrl) != fakeQueueHost+"q-orders-ship" {
		t.Fatalf("sent %d messages, want the record's body sent again to its queue before SQS drops it", len(sent))
	}
	if len(resp.BatchItemFailures) != 0 || len(l.sqs.hidden()) != 0 {
		t.Errorf("Deliver kept %v and hid %v, want the record acknowledged now that its copy waits on the queue", resp.BatchItemFailures, l.sqs.hidden())
	}
}
