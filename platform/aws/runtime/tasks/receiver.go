package tasks

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	longestReceiveWait = 20 * time.Second
	cronRetryAfter     = 5 * time.Second
)

type receivingQueues interface {
	queueService
	ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessageBatch(ctx context.Context, in *sqs.DeleteMessageBatchInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error)
	GetQueueAttributes(ctx context.Context, in *sqs.GetQueueAttributesInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueAttributesOutput, error)
}

type Receiver struct {
	queue    *Queue
	client   receivingQueues
	fireCron func(ctx context.Context, task string, scheduled time.Time) error

	mu       sync.Mutex
	received map[string]receipt
	lastBusy time.Time
}

type receipt struct {
	body       string
	sentAt     time.Time
	receivedAt time.Time
}

func NewReceiver(queue *Queue, client receivingQueues, fireCron func(ctx context.Context, task string, scheduled time.Time) error) *Receiver {
	return &Receiver{queue: queue, client: client, fireCron: fireCron, received: map[string]receipt{}, lastBusy: time.Now()}
}

func (r *Receiver) locate(ctx context.Context, topic, consumer string) (string, string, error) {
	name, err := r.queue.queueOf(topic, consumer)
	if err != nil {
		return "", "", err
	}
	url, err := r.queue.url(ctx, name)
	return name, url, err
}

func (r *Receiver) urlOf(ctx context.Context, topic, consumer string) (string, error) {
	_, url, err := r.locate(ctx, topic, consumer)
	return url, err
}

func (r *Receiver) Receive(ctx context.Context, req runs.ReceiveRequest) ([]runs.Received, error) {
	url, err := r.urlOf(ctx, req.Topic, req.Consumer)
	if err != nil {
		return nil, err
	}
	out, err := r.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:                    aws.String(url),
		MaxNumberOfMessages:         int32(min(max(req.Max, 1), maxSQSBatch)),
		WaitTimeSeconds:             ceilSeconds(min(req.Wait, longestReceiveWait)),
		VisibilityTimeout:           ceilSeconds(min(req.Hidden, maxVisibility)),
		MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameApproximateReceiveCount, sqstypes.MessageSystemAttributeNameSentTimestamp},
	})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var received []runs.Received
	receipts := map[string]receipt{}
	for _, message := range out.Messages {
		handle, body := aws.ToString(message.ReceiptHandle), aws.ToString(message.Body)
		if marker, scheduled := readCronMarker(body); scheduled {
			r.fire(ctx, url, handle, marker)
			continue
		}
		msg, err := messageOf(req.Topic, req.Consumer, body)
		if err != nil {
			slog.Warn("read a received message", "topic", req.Topic, "consumer", req.Consumer, "message", aws.ToString(message.MessageId), "error", err)
			continue
		}
		received = append(received, runs.Received{Handle: handle, Message: msg, Receives: receiveCount(message.Attributes)})
		receipts[handle] = receipt{body: body, sentAt: sentAtOf(message.Attributes), receivedAt: now}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for handle, held := range receipts {
		r.received[handle] = held
	}
	if len(out.Messages) > 0 {
		r.lastBusy = now
	}
	return received, nil
}

func (r *Receiver) fire(ctx context.Context, url, handle string, marker cronMarker) {
	if err := r.fireCron(ctx, marker.Cron, marker.scheduledAt()); err != nil {
		slog.Warn("start a task on its schedule", "task", marker.Cron, "error", err)
		r.queue.changeVisibility(context.WithoutCancel(ctx), url, []visibilityChange{{handle: handle, seconds: ceilSeconds(cronRetryAfter)}})
		return
	}
	r.delete(context.WithoutCancel(ctx), url, []string{handle})
}

func (r *Receiver) forget(handles []string) map[string]receipt {
	r.mu.Lock()
	defer r.mu.Unlock()
	forgotten := make(map[string]receipt, len(handles))
	for _, handle := range handles {
		if held, found := r.received[handle]; found {
			forgotten[handle] = held
		}
		delete(r.received, handle)
	}
	r.lastBusy = time.Now()
	return forgotten
}

func (r *Receiver) receiptOf(handle string) (receipt, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	held, found := r.received[handle]
	return held, found
}

func (r *Receiver) Extend(ctx context.Context, topic, consumer string, handles []string, hidden time.Duration) error {
	url, err := r.urlOf(ctx, topic, consumer)
	if err != nil {
		return err
	}
	now := time.Now()
	var changes []visibilityChange
	for _, handle := range handles {
		held, found := r.receiptOf(handle)
		if !found {
			continue
		}
		if change, changeable := visibilityOf(handle, hidden, held.receivedAt, now); changeable {
			changes = append(changes, change)
		}
	}
	r.queue.changeVisibility(ctx, url, changes)
	return nil
}

func (r *Receiver) Ack(ctx context.Context, topic, consumer string, handles []string) error {
	url, err := r.urlOf(ctx, topic, consumer)
	if err != nil {
		return err
	}
	r.forget(handles)
	r.delete(ctx, url, handles)
	return nil
}

func (r *Receiver) delete(ctx context.Context, url string, handles []string) {
	for chunk := range slices.Chunk(handles, maxSQSBatch) {
		entries := make([]sqstypes.DeleteMessageBatchRequestEntry, 0, len(chunk))
		for i, handle := range chunk {
			entries = append(entries, sqstypes.DeleteMessageBatchRequestEntry{Id: aws.String(strconv.Itoa(i)), ReceiptHandle: aws.String(handle)})
		}
		out, err := r.client.DeleteMessageBatch(ctx, &sqs.DeleteMessageBatchInput{QueueUrl: aws.String(url), Entries: entries})
		if err != nil {
			slog.Warn("delete the messages a delivery is finished with", "error", err)
			continue
		}
		for _, failed := range out.Failed {
			slog.Warn("delete a message a delivery is finished with", "code", aws.ToString(failed.Code), "message", aws.ToString(failed.Message))
		}
	}
}

func (r *Receiver) Hide(ctx context.Context, topic, consumer, handle string, hidden time.Duration) error {
	name, url, err := r.locate(ctx, topic, consumer)
	if err != nil {
		return err
	}
	now := time.Now()
	held, found := r.forget([]string{handle})[handle]
	if !found {
		held.receivedAt = now
	}
	if found && outlivesRetention(name, held.sentAt, hidden, now) {
		err := r.queue.sendAgain(ctx, url, held.body, hidden)
		if err == nil {
			r.delete(ctx, url, []string{handle})
			return nil
		}
		slog.Warn("send a message again before its queue drops it", "topic", topic, "consumer", consumer, "error", err)
	}
	if change, changeable := visibilityOf(handle, hidden, held.receivedAt, now); changeable {
		r.queue.changeVisibility(ctx, url, []visibilityChange{change})
	}
	return nil
}

func (r *Receiver) idleSince() (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastBusy, len(r.received) == 0
}
