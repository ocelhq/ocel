package tasks

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

type scriptedQueue struct {
	mu        sync.Mutex
	waiting   []sqstypes.Message
	received  []*sqs.ReceiveMessageInput
	deleted   []string
	extended  []sqstypes.ChangeMessageVisibilityBatchRequestEntry
	extending chan struct{}
	counts    map[string]string
	queried   int
	failing   func(receive int) error
}

func (q *scriptedQueue) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	q.mu.Lock()
	q.received = append(q.received, in)
	if q.failing != nil {
		if err := q.failing(len(q.received)); err != nil {
			q.mu.Unlock()
			return nil, err
		}
	}
	take := min(len(q.waiting), int(in.MaxNumberOfMessages))
	out := &sqs.ReceiveMessageOutput{Messages: append([]sqstypes.Message(nil), q.waiting[:take]...)}
	q.waiting = q.waiting[take:]
	q.mu.Unlock()
	if take == 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return out, nil
}

func (q *scriptedQueue) DeleteMessageBatch(_ context.Context, in *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, entry := range in.Entries {
		q.deleted = append(q.deleted, aws.ToString(entry.ReceiptHandle))
	}
	return &sqs.DeleteMessageBatchOutput{}, nil
}

func (q *scriptedQueue) ChangeMessageVisibilityBatch(_ context.Context, in *sqs.ChangeMessageVisibilityBatchInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityBatchOutput, error) {
	q.mu.Lock()
	q.extended = append(q.extended, in.Entries...)
	extending := q.extending
	q.mu.Unlock()
	if extending != nil {
		select {
		case extending <- struct{}{}:
		default:
		}
	}
	return &sqs.ChangeMessageVisibilityBatchOutput{}, nil
}

func (q *scriptedQueue) GetQueueAttributes(_ context.Context, in *sqs.GetQueueAttributesInput, _ ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.queried++
	attributes := map[string]string{}
	for _, name := range in.AttributeNames {
		if count, found := q.counts[string(name)]; found {
			attributes[string(name)] = count
		}
	}
	return &sqs.GetQueueAttributesOutput{Attributes: attributes}, nil
}

func (q *scriptedQueue) GetQueueUrl(_ context.Context, in *sqs.GetQueueUrlInput, _ ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	return &sqs.GetQueueUrlOutput{QueueUrl: aws.String("https://sqs.test/" + aws.ToString(in.QueueName))}, nil
}

func (q *scriptedQueue) SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	return &sqs.SendMessageOutput{}, nil
}

func (q *scriptedQueue) ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

func (q *scriptedQueue) deletedHandles() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.deleted...)
}

func (q *scriptedQueue) extensions() []sqstypes.ChangeMessageVisibilityBatchRequestEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]sqstypes.ChangeMessageVisibilityBatchRequestEntry(nil), q.extended...)
}

func message(id string) sqstypes.Message {
	return sqstypes.Message{
		MessageId:     aws.String(id),
		ReceiptHandle: aws.String("handle-" + id),
		Body:          aws.String(`{"execution":"` + id + `"}`),
		Attributes:    map[string]string{string(sqstypes.MessageSystemAttributeNameApproximateReceiveCount): "2"},
	}
}

func consumingEngine(queue *scriptedQueue, consumers ...provider.ConsumerSpec) *Engine {
	topics := map[string]queues.Topic{}
	for _, consumer := range consumers {
		declared := &provider.TopicSpec{Consumers: []provider.ConsumerSpec{consumer}}
		topics[consumer.Name] = queues.Topic{Declared: declared, Queues: map[string]string{consumer.Name: "q-" + consumer.Name}}
	}
	return New(Config{Topology: queues.Topology{Topics: topics}, Queues: queue, Worker: "media"})
}

func consumption(e *Engine, queue *scriptedQueue, deliver func(context.Context, events.SQSEvent) events.SQSEventResponse) *Consumption {
	c := NewConsumption(e, queue)
	c.deliver = deliver
	c.renewEvery = 10 * time.Millisecond
	return c
}

func runUntil(t *testing.T, c *Consumption, done func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- c.Run(ctx) }()
	deadline := time.After(5 * time.Second)
	for !done() {
		select {
		case <-deadline:
			cancel()
			t.Fatal("the condition never held")
		case <-time.After(2 * time.Millisecond):
		}
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatalf("Run() = %v, want nil once told to stop", err)
	}
}

func TestAWorkerDeliversAMessageItReceivedToTheEngineAndDeletesItOnceAcknowledged(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{waiting: []sqstypes.Message{message("a")}}
	var delivered events.SQSEvent
	c := consumption(consumingEngine(queue, provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true}), queue,
		func(_ context.Context, event events.SQSEvent) events.SQSEventResponse {
			delivered = event
			return events.SQSEventResponse{}
		})

	runUntil(t, c, func() bool { return len(queue.deletedHandles()) == 1 })

	if got := queue.deletedHandles(); got[0] != "handle-a" {
		t.Errorf("deleted %v, want the receipt handle of the acknowledged message", got)
	}
	record := delivered.Records[0]
	if queueOf(record.EventSourceARN) != "q-resize" || record.MessageId != "a" || record.ReceiptHandle != "handle-a" || receiveCount(record) != 2 {
		t.Errorf("the engine was handed %+v, want the message as the queue trigger hands it: its queue, id, handle and receive count", record)
	}
	received := queue.received[0]
	if aws.ToString(received.QueueUrl) != "https://sqs.test/q-resize" || received.WaitTimeSeconds != 20 || received.VisibilityTimeout != 120 {
		t.Errorf("received with %+v, want a 20s long poll that hides the message for two minutes", received)
	}
}

func TestAMessageTheEngineRetainsIsNotDeletedAndComesBackSoon(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{waiting: []sqstypes.Message{message("a"), message("b")}}
	c := consumption(consumingEngine(queue, provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true}), queue,
		func(_ context.Context, event events.SQSEvent) events.SQSEventResponse {
			if event.Records[0].MessageId == "a" {
				return events.SQSEventResponse{BatchItemFailures: []events.SQSBatchItemFailure{{ItemIdentifier: "a"}}}
			}
			return events.SQSEventResponse{}
		})

	runUntil(t, c, func() bool {
		for _, entry := range queue.extensions() {
			if aws.ToString(entry.ReceiptHandle) == "handle-a" && entry.VisibilityTimeout == 5 {
				return len(queue.deletedHandles()) == 1
			}
		}
		return false
	})

	for _, handle := range queue.deletedHandles() {
		if handle == "handle-a" {
			t.Error("a message the engine retained was deleted, so its run would never be tried again")
		}
	}
}

func TestAMessageBeingWorkedIsKeptHiddenUntilTheEngineIsDone(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{waiting: []sqstypes.Message{message("a")}, extending: make(chan struct{}, 1)}
	release := make(chan struct{})
	c := consumption(consumingEngine(queue, provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true}), queue,
		func(ctx context.Context, _ events.SQSEvent) events.SQSEventResponse {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return events.SQSEventResponse{}
		})

	var released bool
	runUntil(t, c, func() bool {
		if !released {
			select {
			case <-queue.extending:
				released = true
				close(release)
			default:
			}
		}
		return released && len(queue.deletedHandles()) == 1
	})

	for _, entry := range queue.extensions() {
		if aws.ToString(entry.ReceiptHandle) != "handle-a" || entry.VisibilityTimeout != 120 {
			t.Errorf("extended %+v, want the working message hidden for two minutes more", entry)
		}
	}
}

func TestAMessageTheEngineParkedOnItsQueueIsNotExtendedBackIntoView(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{waiting: []sqstypes.Message{message("a")}}
	e := consumingEngine(queue, provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true})
	release := make(chan struct{})
	e.parking = true
	e.park("handle-a")
	c := consumption(e, queue, func(ctx context.Context, event events.SQSEvent) events.SQSEventResponse {
		time.Sleep(60 * time.Millisecond)
		close(release)
		return events.SQSEventResponse{BatchItemFailures: []events.SQSBatchItemFailure{{ItemIdentifier: "a"}}}
	})

	runUntil(t, c, func() bool {
		select {
		case <-release:
			return true
		default:
			return false
		}
	})

	if extended := queue.extensions(); len(extended) != 0 {
		t.Errorf("extended %+v a message the engine had hidden for its own longer wait, cutting that wait short", extended)
	}
}

func TestAMessageIsNeverExtendedPastTwelveHoursFromItsReceipt(t *testing.T) {
	t.Parallel()

	received := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		now      time.Time
		want     int32
		extended bool
	}{
		"an hour in":         {now: received.Add(time.Hour), want: 120, extended: true},
		"a minute before":    {now: received.Add(12*time.Hour - time.Minute), want: 60, extended: true},
		"at twelve hours":    {now: received.Add(12 * time.Hour), extended: false},
		"after twelve hours": {now: received.Add(13 * time.Hour), extended: false},
	} {
		got, ok := extensionAt(received, tc.now)
		if ok != tc.extended || (ok && int32(got/time.Second) != tc.want) {
			t.Errorf("%s: extensionAt = %v, %v, want %ds, %v", name, got, ok, tc.want, tc.extended)
		}
	}
}

func TestAConsumerWithAConcurrencyHasThatManyMessagesInFlightAtMost(t *testing.T) {
	t.Parallel()

	var waiting []sqstypes.Message
	for i := range 6 {
		waiting = append(waiting, message(strconv.Itoa(i)))
	}
	queue := &scriptedQueue{waiting: waiting}
	var inflight, peak atomic.Int32
	release := make(chan struct{})
	c := consumption(consumingEngine(queue, provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true, Concurrency: 2}), queue,
		func(ctx context.Context, _ events.SQSEvent) events.SQSEventResponse {
			now := inflight.Add(1)
			defer inflight.Add(-1)
			for {
				seen := peak.Load()
				if now <= seen || peak.CompareAndSwap(seen, now) {
					break
				}
			}
			select {
			case <-release:
			case <-ctx.Done():
			}
			return events.SQSEventResponse{}
		})

	runUntil(t, c, func() bool {
		if inflight.Load() == 2 {
			time.Sleep(50 * time.Millisecond)
			defer close(release)
			return peak.Load() == 2
		}
		return false
	})
}

func TestAWorkerPollsOnlyTheQueuesOfTheConsumersItServes(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{}
	c := consumption(consumingEngine(queue,
		provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true},
		provider.ConsumerSpec{Name: "invoice", Worker: "billing", Exclusive: true},
	), queue, func(context.Context, events.SQSEvent) events.SQSEventResponse { return events.SQSEventResponse{} })

	runUntil(t, c, func() bool { queue.mu.Lock(); defer queue.mu.Unlock(); return len(queue.received) > 2 })

	for _, in := range queue.received {
		if aws.ToString(in.QueueUrl) != "https://sqs.test/q-resize" {
			t.Errorf("polled %s, a queue another worker serves", aws.ToString(in.QueueUrl))
		}
	}
}

func TestAnAttemptIsGivenItsMaxDurationAndNoMoreBeforeTheEngineSettlesIt(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{waiting: []sqstypes.Message{message("a")}}
	var window time.Duration
	var started time.Time
	c := consumption(consumingEngine(queue, provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true, MaxDuration: 3 * time.Hour}), queue,
		func(ctx context.Context, _ events.SQSEvent) events.SQSEventResponse {
			started = time.Now()
			deadline, _ := ctx.Deadline()
			window = deadline.Sub(started)
			return events.SQSEventResponse{}
		})

	runUntil(t, c, func() bool { return len(queue.deletedHandles()) == 1 })

	if window < 3*time.Hour || window > 3*time.Hour+time.Minute {
		t.Errorf("the engine was given %v, want the declared 3h and a short margin to settle: a crashed attempt holds its run for as long as that window", window)
	}
}

func TestAConsumerDeclaringNoMaxDurationMayRunTheTwelveHoursSQSAllows(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{waiting: []sqstypes.Message{message("a")}}
	var window time.Duration
	c := consumption(consumingEngine(queue, provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true}), queue,
		func(ctx context.Context, _ events.SQSEvent) events.SQSEventResponse {
			deadline, _ := ctx.Deadline()
			window = time.Until(deadline)
			return events.SQSEventResponse{}
		})

	runUntil(t, c, func() bool { return len(queue.deletedHandles()) == 1 })

	if window < 11*time.Hour+59*time.Minute || window > 12*time.Hour {
		t.Errorf("the engine was given %v, want 12h: the ceiling of a worker on container compute", window)
	}
}

func TestABatchConsumerIsHandedItsBatchSizeAcrossReceivesAndNoMore(t *testing.T) {
	t.Parallel()

	var waiting []sqstypes.Message
	for i := range 25 {
		waiting = append(waiting, message(strconv.Itoa(i)))
	}
	queue := &scriptedQueue{waiting: waiting}
	var sizes []int
	var mu sync.Mutex
	c := consumption(consumingEngine(queue, provider.ConsumerSpec{Name: "tally", Worker: "media", Exclusive: true, Concurrency: 1, Batch: &provider.BatchPolicy{Size: 12, Timeout: time.Second}}), queue,
		func(_ context.Context, event events.SQSEvent) events.SQSEventResponse {
			mu.Lock()
			defer mu.Unlock()
			sizes = append(sizes, len(event.Records))
			return events.SQSEventResponse{}
		})

	runUntil(t, c, func() bool { mu.Lock(); defer mu.Unlock(); return len(sizes) >= 2 })

	mu.Lock()
	defer mu.Unlock()
	if sizes[0] != 12 || sizes[1] != 12 {
		t.Errorf("batches of %v, want twelve at a time, gathered across ten-message receives", sizes)
	}
}

func idleConsumption(queue *scriptedQueue, services *fakeServices, deliver func(context.Context, events.SQSEvent) events.SQSEventResponse) *Consumption {
	e := consumingEngine(queue, provider.ConsumerSpec{Name: "resize", Worker: "media", Exclusive: true})
	e.cfg.Services = services
	e.cfg.Topology.Workers = map[string]queues.Worker{"media": {Service: &queues.Service{Cluster: "ocel-containers-production", Name: "ocel-app-media"}}}
	c := consumption(e, queue, deliver)
	c.idleAfter, c.idleEvery = 30*time.Millisecond, 5*time.Millisecond
	return c
}

func nothingDelivered(context.Context, events.SQSEvent) events.SQSEventResponse {
	return events.SQSEventResponse{}
}

func TestAWorkerWithNothingToDoSetsItsServiceToNoTasksOnceItsQueuesHaveStayedEmpty(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{counts: map[string]string{"ApproximateNumberOfMessages": "0", "ApproximateNumberOfMessagesNotVisible": "0", "ApproximateNumberOfMessagesDelayed": "7"}}
	services := &fakeServices{desired: 1}
	c := idleConsumption(queue, services, nothingDelivered)

	runUntil(t, c, func() bool { services.mu.Lock(); defer services.mu.Unlock(); return len(services.updated) > 0 })

	got := services.updated[0]
	if aws.ToString(got.Cluster) != "ocel-containers-production" || aws.ToString(got.Service) != "ocel-app-media" || aws.ToInt32(got.DesiredCount) != 0 {
		t.Errorf("UpdateService(%s, %s, desired %d), want the worker's own service at none: delayed messages wait unseen and the backlog alarm wakes it when they are due", aws.ToString(got.Cluster), aws.ToString(got.Service), aws.ToInt32(got.DesiredCount))
	}
}

func TestAWorkerStaysUpWhileAnyOfItsQueuesHoldsAMessageVisibleOrInFlightAnywhere(t *testing.T) {
	t.Parallel()

	for name, counts := range map[string]map[string]string{
		"visible":   {"ApproximateNumberOfMessages": "1", "ApproximateNumberOfMessagesNotVisible": "0"},
		"in flight": {"ApproximateNumberOfMessages": "0", "ApproximateNumberOfMessagesNotVisible": "3"},
	} {
		queue := &scriptedQueue{counts: counts}
		services := &fakeServices{desired: 1}
		c := idleConsumption(queue, services, nothingDelivered)

		runUntil(t, c, func() bool { queue.mu.Lock(); defer queue.mu.Unlock(); return queue.queried >= 3 })

		if len(services.updated) != 0 {
			t.Errorf("%s: the service was set to %d tasks while a queue still held a message, which another task may be working", name, aws.ToInt32(services.updated[0].DesiredCount))
		}
	}
}

func TestAWorkerDoesNotSleepWhileItIsWorkingAMessage(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{waiting: []sqstypes.Message{message("a")}, counts: map[string]string{"ApproximateNumberOfMessages": "0", "ApproximateNumberOfMessagesNotVisible": "0"}}
	services := &fakeServices{desired: 1}
	working := make(chan struct{})
	c := idleConsumption(queue, services, func(ctx context.Context, _ events.SQSEvent) events.SQSEventResponse {
		time.Sleep(150 * time.Millisecond)
		close(working)
		return events.SQSEventResponse{}
	})

	runUntil(t, c, func() bool {
		select {
		case <-working:
			return true
		default:
			return false
		}
	})

	if len(services.updated) != 0 {
		t.Error("the service was set to no tasks in the middle of an attempt, which would have killed it")
	}
}

func TestMessagesGatheredForABatchAreGivenBackWhenTheWorkerIsToldToStopBeforeTheyAreDelivered(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{waiting: []sqstypes.Message{message("a"), message("b"), message("c")}}
	c := consumption(consumingEngine(queue, provider.ConsumerSpec{Name: "tally", Worker: "media", Exclusive: true, Concurrency: 1, Batch: &provider.BatchPolicy{Size: 12, Timeout: time.Minute}}), queue,
		func(context.Context, events.SQSEvent) events.SQSEventResponse {
			t.Error("a batch that never filled was delivered after the worker was told to stop")
			return events.SQSEventResponse{}
		})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- c.Run(ctx) }()
	for {
		queue.mu.Lock()
		gathered := len(queue.waiting) == 0 && len(queue.received) >= 2
		queue.mu.Unlock()
		if gathered {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-finished

	given := map[string]bool{}
	for _, entry := range queue.extensions() {
		if entry.VisibilityTimeout == 0 {
			given[aws.ToString(entry.ReceiptHandle)] = true
		}
	}
	if len(given) != 3 {
		t.Errorf("given back %v, want all three gathered messages visible at once rather than hidden for two minutes", given)
	}
}

func TestMessagesGatheredForABatchAreGivenBackWhenAReceiveFailsBeforeTheBatchFills(t *testing.T) {
	t.Parallel()

	queue := &scriptedQueue{
		waiting: []sqstypes.Message{message("a"), message("b"), message("c")},
		failing: func(receive int) error {
			if receive == 2 {
				return errors.New("ServiceUnavailable")
			}
			return nil
		},
	}
	c := consumption(consumingEngine(queue, provider.ConsumerSpec{Name: "tally", Worker: "media", Exclusive: true, Concurrency: 1, Batch: &provider.BatchPolicy{Size: 12, Timeout: time.Minute}}), queue, nothingDelivered)

	runUntil(t, c, func() bool {
		given := map[string]bool{}
		for _, entry := range queue.extensions() {
			if entry.VisibilityTimeout == 0 {
				given[aws.ToString(entry.ReceiptHandle)] = true
			}
		}
		return len(given) == 3
	})
}
