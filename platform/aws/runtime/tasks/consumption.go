package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const (
	longPoll            = 20 * time.Second
	hiddenFor           = 2 * time.Minute
	renewEvery          = 40 * time.Second
	retainedHiddenFor   = 5 * time.Second
	settleWindow        = 30 * time.Second
	defaultInFlight     = 10
	maxInFlight         = 50
	maxSQSBatch         = 10
	firstReceiveBackoff = time.Second
	longestReceiveWait  = 30 * time.Second
	finishingBudget     = 30 * time.Second
	idleAfter           = 5 * time.Minute
	idleEvery           = time.Minute
)

type receivingQueues interface {
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(ctx context.Context, in *sqs.DeleteMessageBatchInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
	ChangeMessageVisibilityBatch(ctx context.Context, in *sqs.ChangeMessageVisibilityBatchInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityBatchOutput, error)
	GetQueueAttributes(ctx context.Context, in *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

type Consumption struct {
	engine     *Engine
	queues     receivingQueues
	deliver    func(ctx context.Context, event events.SQSEvent) events.SQSEventResponse
	renewEvery time.Duration
	idleAfter  time.Duration
	idleEvery  time.Duration

	mu       sync.Mutex
	lastBusy time.Time
	working  int
}

func NewConsumption(engine *Engine, queues receivingQueues) *Consumption {
	engine.pollsItsQueues = true
	return &Consumption{engine: engine, queues: queues, deliver: engine.Deliver, renewEvery: renewEvery, idleAfter: idleAfter, idleEvery: idleEvery, lastBusy: time.Now()}
}

func (c *Consumption) Run(ctx context.Context) error {
	served := c.engine.consumersOf(c.engine.cfg.Worker)
	if len(served) == 0 {
		return fmt.Errorf("worker %q serves no queue: no consumer in the deployment's topics runs on it", c.engine.cfg.Worker)
	}
	var running sync.WaitGroup
	urls := make([]string, 0, len(served))
	for _, deployed := range served {
		url, err := c.engine.queueURL(ctx, deployed.queue)
		if err != nil {
			return err
		}
		urls = append(urls, url)
		for range inFlight(deployed.consumer.Concurrency, c.engine.cfg.Topology.Workers[deployed.consumer.Worker].Concurrency) {
			running.Add(1)
			go func() {
				defer running.Done()
				c.serve(ctx, deployed, url)
			}()
		}
	}
	running.Add(1)
	go func() {
		defer running.Done()
		c.keepLeases(ctx)
	}()
	if c.engine.canScaleToNone() {
		running.Add(1)
		go func() {
			defer running.Done()
			c.scaleToNoneWhenIdle(ctx, urls)
		}()
	}
	running.Wait()
	return nil
}

func (c *Consumption) markBusy(by int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.working += by
	c.lastBusy = time.Now()
}

func (c *Consumption) idleSince() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastBusy, c.working == 0
}

func (c *Consumption) scaleToNoneWhenIdle(ctx context.Context, urls []string) {
	ticker := time.NewTicker(c.idleEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		busySince, idle := c.idleSince()
		if !idle || time.Since(busySince) < c.idleAfter {
			continue
		}
		holding, err := c.holdsMessages(ctx, urls)
		if err != nil {
			slog.Warn("read what a worker's queues hold", "error", err)
			continue
		}
		if holding {
			continue
		}
		if err := c.engine.scaleToNone(ctx); err != nil {
			slog.Warn("set an idle worker's service to no tasks", "error", err)
			continue
		}
		c.markBusy(0)
	}
}

func (c *Consumption) holdsMessages(ctx context.Context, urls []string) (bool, error) {
	for _, url := range urls {
		out, err := c.queues.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(url),
			AttributeNames: []sqstypes.QueueAttributeName{
				sqstypes.QueueAttributeNameApproximateNumberOfMessages,
				sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
			},
		})
		if err != nil {
			return false, err
		}
		for _, count := range out.Attributes {
			if n, err := strconv.Atoi(count); err != nil || n > 0 {
				return true, nil
			}
		}
	}
	return false, nil
}

func inFlight(consumer, worker int) int {
	limit := defaultInFlight
	if consumer > 0 {
		limit = consumer
	}
	if worker > 0 {
		limit = min(limit, worker)
	}
	return min(limit, maxInFlight)
}

func (e *Engine) consumersOf(worker string) []deployedConsumer {
	var served []deployedConsumer
	for name, topic := range e.cfg.Topology.Topics {
		if topic.Declared == nil {
			continue
		}
		for _, consumer := range topic.Declared.Consumers {
			if consumer.Worker == worker {
				served = append(served, deployedConsumer{topicName: name, topic: topic.Declared, consumer: consumer, queue: topic.Queues[consumer.Name]})
			}
		}
	}
	slices.SortFunc(served, func(a, b deployedConsumer) int {
		if a.queue < b.queue {
			return -1
		}
		if a.queue > b.queue {
			return 1
		}
		return 0
	})
	return served
}

type receivedMessage struct {
	record     events.SQSMessage
	receivedAt time.Time
}

type holding struct {
	mu       sync.Mutex
	messages []receivedMessage
}

func (h *holding) add(messages []receivedMessage) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, messages...)
}

func (h *holding) all() []receivedMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.messages)
}

func (c *Consumption) serve(ctx context.Context, deployed deployedConsumer, url string) {
	backoff := firstReceiveBackoff
	for ctx.Err() == nil {
		held := &holding{}
		stopRenewing, err := c.gather(ctx, deployed, url, held)
		if err != nil {
			c.giveBack(ctx, url, held.all())
			if ctx.Err() != nil {
				return
			}
			slog.Warn("receive from a queue", "queue", deployed.queue, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff/2 + rand.N(backoff/2+1)):
			}
			backoff = min(backoff*2, longestReceiveWait)
			continue
		}
		backoff = firstReceiveBackoff
		messages := held.all()
		if len(messages) == 0 {
			continue
		}
		c.work(ctx, deployed, messages, stopRenewing)
	}
}

func (c *Consumption) giveBack(ctx context.Context, url string, messages []receivedMessage) {
	if len(messages) == 0 {
		return
	}
	budget, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishingBudget)
	defer cancel()
	c.hide(budget, url, hiddenAlike(messages, 0))
}

func (c *Consumption) gather(ctx context.Context, deployed deployedConsumer, url string, held *holding) (stop func(), err error) {
	size, window := 1, time.Duration(0)
	if batch := deployed.consumer.Batch; batch != nil && batch.Size > 0 {
		size, window = batch.Size, batch.Timeout
	}
	var began time.Time
	stop = func() {}
	for {
		gathered := len(held.all())
		wait := longPoll
		if gathered > 0 {
			remaining := window - time.Since(began)
			if gathered >= size || remaining <= 0 {
				return stop, nil
			}
			wait = min(longPoll, remaining)
		}
		out, err := c.queues.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(url),
			MaxNumberOfMessages:         int32(min(size-gathered, maxSQSBatch)),
			WaitTimeSeconds:             int32(wait / time.Second),
			VisibilityTimeout:           int32(hiddenFor / time.Second),
			MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			stop()
			return func() {}, err
		}
		if len(out.Messages) == 0 {
			if gathered == 0 {
				return stop, nil
			}
			continue
		}
		now := time.Now()
		c.markBusy(0)
		received := make([]receivedMessage, 0, len(out.Messages))
		for _, message := range out.Messages {
			received = append(received, receivedMessage{record: recordOf(deployed.queue, message), receivedAt: now})
		}
		if gathered == 0 {
			began = now
			renewing, cancelRenewing := context.WithCancel(ctx)
			renewed := make(chan struct{})
			held.add(received)
			go func() {
				defer close(renewed)
				c.keepHidden(renewing, url, held)
			}()
			stop = func() {
				cancelRenewing()
				<-renewed
			}
			continue
		}
		held.add(received)
	}
}

func recordOf(queue string, message sqstypes.Message) events.SQSMessage {
	return events.SQSMessage{
		MessageId:      aws.ToString(message.MessageId),
		ReceiptHandle:  aws.ToString(message.ReceiptHandle),
		Body:           aws.ToString(message.Body),
		Attributes:     message.Attributes,
		EventSourceARN: "arn:aws:sqs:::" + queue,
	}
}

func (c *Consumption) work(ctx context.Context, deployed deployedConsumer, messages []receivedMessage, stopRenewing func()) {
	c.markBusy(1)
	defer c.markBusy(-1)
	records := make([]events.SQSMessage, 0, len(messages))
	for _, message := range messages {
		records = append(records, message.record)
	}
	budget := deployed.consumer.MaxDuration + settleWindow
	if deployed.consumer.MaxDuration <= 0 {
		budget = maxVisibilityDelay
	}
	deadline := time.Now().Add(budget)
	deliveryCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	response := c.deliver(deliveryCtx, events.SQSEvent{Records: records})
	stopRenewing()

	retained := map[string]bool{}
	for _, failure := range response.BatchItemFailures {
		retained[failure.ItemIdentifier] = true
	}
	finishing, finished := context.WithTimeout(context.WithoutCancel(ctx), finishingBudget)
	defer finished()
	again := retainedHiddenFor
	if ctx.Err() != nil {
		again = 0
	}
	now := time.Now()
	var done []receivedMessage
	var hidden []visibilityChange
	for _, message := range messages {
		until, held := c.engine.takeHold(message.record.ReceiptHandle)
		switch {
		case !retained[message.record.MessageId]:
			done = append(done, message)
		case held:
			hidden = append(hidden, visibilityChange{message: message, hiddenFor: holdAt(message.receivedAt, until, now)})
		default:
			hidden = append(hidden, visibilityChange{message: message, hiddenFor: again})
		}
	}
	url, err := c.engine.queueURL(finishing, deployed.queue)
	if err != nil {
		slog.Warn("finish with a queue's messages", "queue", deployed.queue, "error", err)
		return
	}
	c.delete(finishing, url, done)
	c.hide(finishing, url, hidden)
}

func holdAt(received, until, now time.Time) time.Duration {
	return max(0, min(until.Sub(now), received.Add(maxVisibilityDelay).Sub(now)))
}

type visibilityChange struct {
	message   receivedMessage
	hiddenFor time.Duration
}

func hiddenAlike(messages []receivedMessage, hiddenFor time.Duration) []visibilityChange {
	changes := make([]visibilityChange, 0, len(messages))
	for _, message := range messages {
		changes = append(changes, visibilityChange{message: message, hiddenFor: hiddenFor})
	}
	return changes
}

func (c *Consumption) delete(ctx context.Context, url string, messages []receivedMessage) {
	for chunk := range slices.Chunk(messages, maxSQSBatch) {
		entries := make([]sqstypes.DeleteMessageBatchRequestEntry, 0, len(chunk))
		for i, message := range chunk {
			entries = append(entries, sqstypes.DeleteMessageBatchRequestEntry{Id: aws.String(strconv.Itoa(i)), ReceiptHandle: aws.String(message.record.ReceiptHandle)})
		}
		out, err := c.queues.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(url), Entries: entries})
		if err != nil {
			slog.Warn("delete acknowledged messages", "error", err)
			continue
		}
		for _, failed := range out.Failed {
			slog.Warn("delete an acknowledged message", "code", aws.ToString(failed.Code), "message", aws.ToString(failed.Message))
		}
	}
}

func (c *Consumption) hide(ctx context.Context, url string, changes []visibilityChange) {
	for chunk := range slices.Chunk(changes, maxSQSBatch) {
		entries := make([]sqstypes.ChangeMessageVisibilityBatchRequestEntry, 0, len(chunk))
		for i, change := range chunk {
			entries = append(entries, sqstypes.ChangeMessageVisibilityBatchRequestEntry{
				Id:                aws.String(strconv.Itoa(i)),
				ReceiptHandle:     aws.String(change.message.record.ReceiptHandle),
				VisibilityTimeout: int32(change.hiddenFor / time.Second),
			})
		}
		out, err := c.queues.ChangeMessageVisibilityBatch(ctx, &sqs.ChangeMessageVisibilityBatchInput{QueueUrl: aws.String(url), Entries: entries})
		if err != nil {
			slog.Warn("change what messages are hidden", "error", err)
			continue
		}
		for _, failed := range out.Failed {
			slog.Warn("change what a message is hidden for", "code", aws.ToString(failed.Code), "message", aws.ToString(failed.Message))
		}
	}
}

func extensionAt(received, now time.Time) (time.Duration, bool) {
	remaining := received.Add(maxVisibilityDelay).Sub(now)
	if remaining <= 0 {
		return 0, false
	}
	return min(hiddenFor, remaining), true
}

func (c *Consumption) keepHidden(ctx context.Context, url string, held *holding) {
	ticker := time.NewTicker(c.renewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := time.Now()
		var entries []sqstypes.ChangeMessageVisibilityBatchRequestEntry
		for _, message := range held.all() {
			extension, extend := extensionAt(message.receivedAt, now)
			if !extend {
				continue
			}
			entries = append(entries, sqstypes.ChangeMessageVisibilityBatchRequestEntry{
				Id:                aws.String(strconv.Itoa(len(entries))),
				ReceiptHandle:     aws.String(message.record.ReceiptHandle),
				VisibilityTimeout: int32(extension / time.Second),
			})
		}
		for chunk := range slices.Chunk(entries, maxSQSBatch) {
			out, err := c.queues.ChangeMessageVisibilityBatch(ctx, &sqs.ChangeMessageVisibilityBatchInput{QueueUrl: aws.String(url), Entries: chunk})
			if err != nil {
				slog.Warn("keep messages hidden while they are worked", "error", err)
				continue
			}
			for _, failed := range out.Failed {
				slog.Warn("keep a message hidden while it is worked", "code", aws.ToString(failed.Code), "message", aws.ToString(failed.Message))
			}
		}
	}
}
