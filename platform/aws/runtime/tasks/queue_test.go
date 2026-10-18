package tasks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const fakeQueueHost = "https://sqs.test/"

type fakeSQS struct {
	mu       sync.Mutex
	waiting  map[string][]sqstypes.Message
	received []*sqs.ReceiveMessageInput
	sent     []*sqs.SendMessageInput
	deletes  [][]string
	hides    [][]sqstypes.ChangeMessageVisibilityBatchRequestEntry
	released []*sqs.ChangeMessageVisibilityInput
	counts   map[string]string
	queried  int
}

func newFakeSQS() *fakeSQS {
	return &fakeSQS{waiting: map[string][]sqstypes.Message{}, counts: map[string]string{}}
}

func (f *fakeSQS) GetQueueUrl(_ context.Context, in *sqs.GetQueueUrlInput, _ ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	return &sqs.GetQueueUrlOutput{QueueUrl: aws.String(fakeQueueHost + aws.ToString(in.QueueName))}, nil
}

func (f *fakeSQS) SendMessage(_ context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, in)
	return &sqs.SendMessageOutput{}, nil
}

func (f *fakeSQS) ChangeMessageVisibility(_ context.Context, in *sqs.ChangeMessageVisibilityInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, in)
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func (f *fakeSQS) ChangeMessageVisibilityBatch(_ context.Context, in *sqs.ChangeMessageVisibilityBatchInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityBatchOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hides = append(f.hides, in.Entries)
	return &sqs.ChangeMessageVisibilityBatchOutput{}, nil
}

func (f *fakeSQS) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	f.mu.Lock()
	f.received = append(f.received, in)
	url := aws.ToString(in.QueueUrl)
	take := min(len(f.waiting[url]), int(in.MaxNumberOfMessages))
	out := &sqs.ReceiveMessageOutput{Messages: append([]sqstypes.Message(nil), f.waiting[url][:take]...)}
	f.waiting[url] = f.waiting[url][take:]
	f.mu.Unlock()
	if take == 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return out, nil
}

func (f *fakeSQS) DeleteMessageBatch(_ context.Context, in *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var handles []string
	for _, entry := range in.Entries {
		handles = append(handles, aws.ToString(entry.ReceiptHandle))
	}
	f.deletes = append(f.deletes, handles)
	return &sqs.DeleteMessageBatchOutput{}, nil
}

func (f *fakeSQS) GetQueueAttributes(_ context.Context, in *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queried++
	attributes := map[string]string{}
	for _, name := range in.AttributeNames {
		if count, found := f.counts[string(name)]; found {
			attributes[string(name)] = count
		}
	}
	return &sqs.GetQueueAttributesOutput{Attributes: attributes}, nil
}

func (f *fakeSQS) wait(queue string, messages ...sqstypes.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waiting[fakeQueueHost+queue] = append(f.waiting[fakeQueueHost+queue], messages...)
}

func (f *fakeSQS) sentMessages() []*sqs.SendMessageInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*sqs.SendMessageInput(nil), f.sent...)
}

func (f *fakeSQS) deleted() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var handles []string
	for _, call := range f.deletes {
		handles = append(handles, call...)
	}
	return handles
}

func (f *fakeSQS) hidden() map[string]int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	seconds := map[string]int32{}
	for _, call := range f.hides {
		for _, entry := range call {
			seconds[aws.ToString(entry.ReceiptHandle)] = entry.VisibilityTimeout
		}
	}
	return seconds
}

type fakeSNS struct {
	mu        sync.Mutex
	published []*sns.PublishInput
}

func (f *fakeSNS) Publish(_ context.Context, in *sns.PublishInput, _ ...func(*sns.Options)) (*sns.PublishOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.published = append(f.published, in)
	return &sns.PublishOutput{}, nil
}

func aTask(name string, retry provider.RetryPolicy, fifo bool) queues.Topic {
	queue := "q-" + name
	if fifo {
		queue += ".fifo"
	}
	return queues.Topic{
		Declared: &provider.TopicSpec{Ordered: fifo, Consumers: []provider.ConsumerSpec{{Name: name, Worker: "media", Exclusive: true, Retry: retry}}},
		Queues:   map[string]string{name: queue},
	}
}

func aTopic(name string, ordered bool, consumers ...string) queues.Topic {
	topic := queues.Topic{Declared: &provider.TopicSpec{Ordered: ordered}, Queues: map[string]string{}, SNS: "arn:aws:sns:us-east-1:000000000000:t-" + name}
	for _, consumer := range consumers {
		queue := "q-" + name + "-" + consumer
		if ordered {
			queue += ".fifo"
			topic.SNS += ".fifo"
		}
		topic.Declared.Consumers = append(topic.Declared.Consumers, provider.ConsumerSpec{Name: consumer, Worker: "media", Retry: retrying(3)})
		topic.Queues[consumer] = queue
	}
	return topic
}

func retrying(maxAttempts int) provider.RetryPolicy {
	return provider.RetryPolicy{MaxAttempts: maxAttempts, MinDelay: 4 * time.Second, MaxDelay: 4 * time.Second}
}

func answering(t *testing.T, answer func(r *http.Request) int) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		rw.WriteHeader(answer(r))
		_, _ = rw.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func answerAlways(status int) func(*http.Request) int {
	return func(*http.Request) int { return status }
}

func recordOf(queue string, id string, body string, receives int) events.SQSMessage {
	return events.SQSMessage{
		MessageId:      id,
		ReceiptHandle:  "handle-" + id,
		Body:           body,
		EventSourceARN: "arn:aws:sqs:us-east-1:000000000000:" + queue,
		Attributes:     map[string]string{"ApproximateReceiveCount": strconv.Itoa(receives)},
	}
}

func TestASendToATopicPublishesOnceToItsSNSTopicForEveryConsumer(t *testing.T) {
	t.Parallel()

	queue, topics := newFakeSQS(), &fakeSNS{}
	q := NewQueue(queues.Topology{Topics: map[string]queues.Topic{"orders": aTopic("orders", false, "ship", "bill")}}, queue, topics, nil)

	if err := q.Send(context.Background(), runs.Message{Topic: "orders", ID: "m1", Key: "k", Payload: json.RawMessage(`{"n":1}`)}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(topics.published) != 1 || len(queue.sentMessages()) != 0 {
		t.Fatalf("published %d and sent %d, want one publish to the SNS topic and nothing sent to a queue", len(topics.published), len(queue.sentMessages()))
	}
	in := topics.published[0]
	if aws.ToString(in.TopicArn) != "arn:aws:sns:us-east-1:000000000000:t-orders" || in.MessageGroupId != nil || in.MessageDeduplicationId != nil {
		t.Errorf("published to %s with group %v and deduplication %v, want the standard topic with neither", aws.ToString(in.TopicArn), in.MessageGroupId, in.MessageDeduplicationId)
	}
}

func TestASendToAnOrderedTopicIsGroupedByItsKeyOrElseItsMessageAndDeduplicatedByItsMessage(t *testing.T) {
	t.Parallel()

	topics := &fakeSNS{}
	q := NewQueue(queues.Topology{Topics: map[string]queues.Topic{"ledger": aTopic("ledger", true, "apply")}}, newFakeSQS(), topics, nil)

	for _, msg := range []runs.Message{{Topic: "ledger", ID: "m1", Key: "account-1"}, {Topic: "ledger", ID: "m2"}} {
		if err := q.Send(context.Background(), msg); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	for i, want := range []struct{ group, dedup string }{{"account-1", "m1"}, {"m2", "m2"}} {
		in := topics.published[i]
		if aws.ToString(in.MessageGroupId) != want.group || aws.ToString(in.MessageDeduplicationId) != want.dedup {
			t.Errorf("publish %d went to group %q deduplicated by %q, want %q and %q", i, aws.ToString(in.MessageGroupId), aws.ToString(in.MessageDeduplicationId), want.group, want.dedup)
		}
	}
}

func TestASendToOneConsumerGoesToItsQueueDelayedOnAStandardQueueAndGroupedOnAFIFOOne(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		fifo      bool
		key       string
		due       time.Duration
		wantDelay int32
		wantGroup string
		wantDedup string
	}{
		"standard, due now":       {due: 0},
		"standard, due in 3s":     {due: 3 * time.Second, wantDelay: 3},
		"standard, due in 2h":     {due: 2 * time.Hour, wantDelay: 900},
		"fifo, keyed, due later":  {fifo: true, key: "k", due: 10 * time.Minute, wantGroup: "k", wantDedup: "e-resize.d2"},
		"fifo, unkeyed, due soon": {fifo: true, due: time.Second, wantGroup: "e-resize", wantDedup: "e-resize.d2"},
	} {
		queue := newFakeSQS()
		q := NewQueue(queues.Topology{Topics: map[string]queues.Topic{"resize": aTask("resize", retrying(3), tc.fifo)}}, queue, &fakeSNS{}, nil)
		msg := runs.Message{Topic: "resize", Consumer: "resize", Execution: "e-resize", Delivery: "d2", Key: tc.key}
		if tc.due > 0 {
			msg.DueAt = time.Now().Add(tc.due)
		}
		if err := q.Send(context.Background(), msg); err != nil {
			t.Fatalf("%s: Send: %v", name, err)
		}
		sent := queue.sentMessages()
		if len(sent) != 1 {
			t.Fatalf("%s: sent %d messages, want 1", name, len(sent))
		}
		in := sent[0]
		if !strings.HasSuffix(aws.ToString(in.QueueUrl), "/q-resize"+map[bool]string{true: ".fifo"}[tc.fifo]) {
			t.Errorf("%s: sent to %s, want the consumer's queue", name, aws.ToString(in.QueueUrl))
		}
		if in.DelaySeconds != tc.wantDelay || aws.ToString(in.MessageGroupId) != tc.wantGroup || aws.ToString(in.MessageDeduplicationId) != tc.wantDedup {
			t.Errorf("%s: sent with delay %ds, group %q and deduplication %q, want %ds, %q and %q", name, in.DelaySeconds, aws.ToString(in.MessageGroupId), aws.ToString(in.MessageDeduplicationId), tc.wantDelay, tc.wantGroup, tc.wantDedup)
		}
	}
}

func TestAMessageBodyCarriesItsPayloadByteForByte(t *testing.T) {
	t.Parallel()

	payload := `{ "html": "<b>&</b>", "ratio": 2.0, "id": 9007199254740993 }`
	sent := runs.Message{Execution: "e", Delivery: "d", ID: "m", PublishedAt: time.UnixMicro(1_700_000_000_000_001), DueAt: time.UnixMicro(1_700_000_000_000_002), Key: "k", Lane: provider.LaneHigh, MaxAttempts: 4, Payload: json.RawMessage(payload)}
	body, err := bodyOf(sent)
	if err != nil {
		t.Fatal(err)
	}
	got, err := messageOf("resize", "resize", body)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Payload) != payload {
		t.Errorf("the payload came back as %s, want %s byte for byte", got.Payload, payload)
	}
	got.Topic, got.Consumer, got.Payload, sent.Payload = "", "", nil, nil
	if !got.PublishedAt.Equal(sent.PublishedAt) || !got.DueAt.Equal(sent.DueAt) {
		t.Errorf("the times came back as %v and %v, want %v and %v", got.PublishedAt, got.DueAt, sent.PublishedAt, sent.DueAt)
	}
	got.PublishedAt, got.DueAt, sent.PublishedAt, sent.DueAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	if got.Execution != sent.Execution || got.Delivery != sent.Delivery || got.ID != sent.ID || got.Key != sent.Key || got.Lane != sent.Lane || got.MaxAttempts != sent.MaxAttempts {
		t.Errorf("the message came back as %+v, want %+v", got, sent)
	}
}

func TestReleasingAHeldMessageMakesItVisibleAtOnce(t *testing.T) {
	t.Parallel()

	queue := newFakeSQS()
	q := NewQueue(queues.Topology{Topics: map[string]queues.Topic{"resize": aTask("resize", retrying(3), true)}}, queue, &fakeSNS{}, nil)

	if err := q.Release(context.Background(), "resize", "resize", "receipt-7"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(queue.released) != 1 || aws.ToString(queue.released[0].ReceiptHandle) != "receipt-7" || queue.released[0].VisibilityTimeout != 0 || aws.ToString(queue.released[0].QueueUrl) != fakeQueueHost+"q-resize.fifo" {
		t.Errorf("Release changed %+v, want receipt-7 on q-resize.fifo visible now", queue.released)
	}
}
