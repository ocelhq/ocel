package tasks

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

type firedCron struct {
	task      string
	scheduled time.Time
}

func newTestReceiver(queue *fakeSQS, fire func(task string, scheduled time.Time) error) *Receiver {
	topology := queues.Topology{Topics: map[string]queues.Topic{"resize": aTask("resize", retrying(3), false)}}
	return NewReceiver(NewQueue(topology, queue, &fakeSNS{}, nil), queue, func(_ context.Context, task string, scheduled time.Time) error {
		return fire(task, scheduled)
	})
}

func noCron(string, time.Time) error { return errors.New("no cron fires in this test") }

func sqsMessage(id, body string, receives int) sqstypes.Message {
	return sqstypes.Message{
		MessageId:     aws.String(id),
		ReceiptHandle: aws.String("handle-" + id),
		Body:          aws.String(body),
		Attributes:    map[string]string{string(sqstypes.MessageSystemAttributeNameApproximateReceiveCount): strconv.Itoa(receives)},
	}
}

func TestAReceiveAsksSQSForAtMostTenMessagesHiddenForTheLeaseWithinItsLongPoll(t *testing.T) {
	t.Parallel()

	queue := newFakeSQS()
	r := newTestReceiver(queue, noCron)
	ctx := context.Background()

	for _, req := range []runs.ReceiveRequest{
		{Topic: "resize", Consumer: "resize", Max: 100, Wait: time.Minute, Hidden: 2 * time.Minute},
		{Topic: "resize", Consumer: "resize", Max: 3, Wait: 300 * time.Millisecond, Hidden: 3 * time.Second},
	} {
		if _, err := r.Receive(ctx, req); err != nil {
			t.Fatalf("Receive: %v", err)
		}
	}
	for i, want := range []struct{ most, wait, hidden int32 }{{10, 20, 120}, {3, 1, 3}} {
		in := queue.received[i]
		if in.MaxNumberOfMessages != want.most || in.WaitTimeSeconds != want.wait || in.VisibilityTimeout != want.hidden {
			t.Errorf("receive %d asked for %d messages waiting %ds hidden %ds, want %d, %ds and %ds", i, in.MaxNumberOfMessages, in.WaitTimeSeconds, in.VisibilityTimeout, want.most, want.wait, want.hidden)
		}
		if aws.ToString(in.QueueUrl) != fakeQueueHost+"q-resize" || !slices.Contains(in.MessageSystemAttributeNames, sqstypes.MessageSystemAttributeNameApproximateReceiveCount) {
			t.Errorf("receive %d read %s for %v, want the consumer's queue with each message's receive count", i, aws.ToString(in.QueueUrl), in.MessageSystemAttributeNames)
		}
	}
}

func TestAReceivedMessageIsHandedOverWithItsHandleAndReceiveCountAndAForeignOneIsNot(t *testing.T) {
	t.Parallel()

	queue := newFakeSQS()
	body, err := bodyOf(runs.Message{Execution: "e-resize", Delivery: "d"})
	if err != nil {
		t.Fatal(err)
	}
	queue.wait("q-resize", sqsMessage("a", body, 4), sqsMessage("b", "not json", 1))
	r := newTestReceiver(queue, noCron)

	received, err := r.Receive(context.Background(), runs.ReceiveRequest{Topic: "resize", Consumer: "resize", Max: 10, Wait: time.Second, Hidden: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(received) != 1 {
		t.Fatalf("Receive handed over %d messages, want only the one this engine sent", len(received))
	}
	got := received[0]
	if got.Handle != "handle-a" || got.Receives != 4 || got.Message.Execution != "e-resize" || got.Message.Topic != "resize" || got.Message.Consumer != "resize" {
		t.Errorf("Receive handed over %+v, want handle-a received 4 times naming e-resize of resize", got)
	}
	if _, idle := r.idleSince(); idle {
		t.Error("the receiver reads as idle while it holds a received message")
	}
}

func TestAcknowledgedMessagesAreDeletedTenAtATime(t *testing.T) {
	t.Parallel()

	queue := newFakeSQS()
	r := newTestReceiver(queue, noCron)
	var handles []string
	for i := range 25 {
		handles = append(handles, "h"+strconv.Itoa(i))
	}

	if err := r.Ack(context.Background(), "resize", "resize", handles); err != nil {
		t.Fatalf("Ack: %v", err)
	}
	var sizes []int
	for _, call := range queue.deletes {
		sizes = append(sizes, len(call))
	}
	if !slices.Equal(sizes, []int{10, 10, 5}) || !slices.Equal(queue.deleted(), handles) {
		t.Errorf("Ack deleted in batches of %v, want every handle in batches of 10, 10 and 5", sizes)
	}
}

func TestAMessageIsNeverKeptHiddenPastTwelveHoursFromItsReceipt(t *testing.T) {
	t.Parallel()

	queue := newFakeSQS()
	r := newTestReceiver(queue, noCron)
	now := time.Now()
	r.received["fresh"] = receipt{receivedAt: now}
	r.received["nearly"] = receipt{receivedAt: now.Add(-maxVisibility + 30*time.Second)}
	r.received["past"] = receipt{receivedAt: now.Add(-maxVisibility - time.Minute)}
	ctx := context.Background()

	if err := r.Extend(ctx, "resize", "resize", []string{"fresh", "nearly", "past"}, 2*time.Minute); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	hidden := queue.hidden()
	if hidden["fresh"] != 120 || hidden["nearly"] > 30 || hidden["nearly"] < 28 {
		t.Errorf("Extend hid fresh for %ds and nearly for %ds, want 120s and what is left of nearly's 12 hours", hidden["fresh"], hidden["nearly"])
	}
	if _, changed := hidden["past"]; changed {
		t.Error("Extend asked SQS to hide a message past twelve hours from its receipt, which it refuses")
	}

	if err := r.Hide(ctx, "resize", "resize", "nearly", 48*time.Hour); err != nil {
		t.Fatalf("Hide: %v", err)
	}
	if got := queue.hidden()["nearly"]; got > 30 {
		t.Errorf("Hide asked for %ds, want no more than is left of the 12 hours since the receipt", got)
	}
	if _, held := r.received["nearly"]; held {
		t.Error("a hidden message is still counted as in flight")
	}
}

func TestACronMarkerOnATasksQueueStartsTheTaskAndIsDeletedWithoutReachingTheEngine(t *testing.T) {
	t.Parallel()

	queue := newFakeSQS()
	queue.wait("q-resize", sqsMessage("c", `{"ocelCron":"resize","scheduledTime":"2026-10-12T03:00:41Z"}`, 1))
	var fired []firedCron
	r := newTestReceiver(queue, func(task string, scheduled time.Time) error {
		fired = append(fired, firedCron{task, scheduled})
		return nil
	})

	received, err := r.Receive(context.Background(), runs.ReceiveRequest{Topic: "resize", Consumer: "resize", Max: 10, Wait: time.Second, Hidden: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(received) != 0 {
		t.Errorf("Receive handed the engine %d messages, want the marker kept from it", len(received))
	}
	if len(fired) != 1 || fired[0].task != "resize" || !fired[0].scheduled.Equal(time.Date(2026, 10, 12, 3, 0, 41, 0, time.UTC)) {
		t.Errorf("fired %+v, want resize once at its scheduled time", fired)
	}
	if !slices.Equal(queue.deleted(), []string{"handle-c"}) {
		t.Errorf("deleted %v, want the marker deleted once its task started", queue.deleted())
	}
}

func TestACronMarkerWhoseTaskCannotStartComesBackSoon(t *testing.T) {
	t.Parallel()

	queue := newFakeSQS()
	queue.wait("q-resize", sqsMessage("c", `{"ocelCron":"resize","scheduledTime":"2026-10-12T03:00:41Z"}`, 1))
	r := newTestReceiver(queue, noCron)

	if _, err := r.Receive(context.Background(), runs.ReceiveRequest{Topic: "resize", Consumer: "resize", Max: 10, Wait: time.Second, Hidden: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if len(queue.deleted()) != 0 || queue.hidden()["handle-c"] != int32(cronRetryAfter/time.Second) {
		t.Errorf("the marker was deleted %v and hidden %v, want it kept and back in %v", queue.deleted(), queue.hidden(), cronRetryAfter)
	}
}

func TestAMessageHiddenPastItsQueuesRetentionIsSentAgainRatherThanLost(t *testing.T) {
	t.Parallel()

	queue := newFakeSQS()
	body, err := bodyOf(runs.Message{Execution: "e-resize", Delivery: "d"})
	if err != nil {
		t.Fatal(err)
	}
	aged := sqsMessage("aged", body, 30)
	aged.Attributes[string(sqstypes.MessageSystemAttributeNameSentTimestamp)] = strconv.FormatInt(time.Now().Add(-queues.MessageRetention+6*time.Hour).UnixMilli(), 10)
	fresh := sqsMessage("fresh", body, 1)
	fresh.Attributes[string(sqstypes.MessageSystemAttributeNameSentTimestamp)] = strconv.FormatInt(time.Now().UnixMilli(), 10)
	queue.wait("q-resize", aged, fresh)
	r := newTestReceiver(queue, noCron)
	ctx := context.Background()

	if _, err := r.Receive(ctx, runs.ReceiveRequest{Topic: "resize", Consumer: "resize", Max: 10, Wait: time.Second, Hidden: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(queue.received[0].MessageSystemAttributeNames, sqstypes.MessageSystemAttributeNameSentTimestamp) {
		t.Errorf("Receive asked for %v, want each message's sent time too", queue.received[0].MessageSystemAttributeNames)
	}
	for _, handle := range []string{"handle-aged", "handle-fresh"} {
		if err := r.Hide(ctx, "resize", "resize", handle, maxVisibility); err != nil {
			t.Fatalf("Hide %s: %v", handle, err)
		}
	}

	sent := queue.sentMessages()
	if len(sent) != 1 || aws.ToString(sent[0].MessageBody) != body || sent[0].DelaySeconds != int32(maxMessageDelay/time.Second) {
		t.Fatalf("sent %d messages, want the aged one sent again with its body, delayed the 15 minutes SQS allows", len(sent))
	}
	if !slices.Equal(queue.deleted(), []string{"handle-aged"}) {
		t.Errorf("deleted %v, want the aged message replaced by its copy", queue.deleted())
	}
	hidden := queue.hidden()
	if _, changed := hidden["handle-aged"]; changed || hidden["handle-fresh"] < int32(maxVisibility/time.Second)-1 {
		t.Errorf("hid %v, want only the fresh message hidden, for the 12 hours", hidden)
	}
}
