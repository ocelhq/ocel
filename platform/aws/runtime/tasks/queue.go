package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const (
	maxMessageDelay = 15 * time.Minute
	maxVisibility   = 12 * time.Hour
	maxSQSBatch     = 10
	retentionMargin = time.Hour
)

type queueService interface {
	GetQueueUrl(ctx context.Context, in *sqs.GetQueueUrlInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
	ChangeMessageVisibilityBatch(ctx context.Context, in *sqs.ChangeMessageVisibilityBatchInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityBatchOutput, error)
}

type topicService interface {
	Publish(ctx context.Context, in *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

type Queue struct {
	topology queues.Topology
	queues   queueService
	topics   topicService
	services *workerServices

	mu   sync.Mutex
	urls map[string]string
}

func NewQueue(topology queues.Topology, queueClient queueService, topicClient topicService, services serviceControl) *Queue {
	return &Queue{
		topology: topology,
		queues:   queueClient,
		topics:   topicClient,
		services: newWorkerServices(topology.Workers, services),
		urls:     map[string]string{},
	}
}

func (q *Queue) topic(name string) (queues.Topic, error) {
	topic, found := q.topology.Topics[name]
	if !found || topic.Declared == nil {
		return queues.Topic{}, fmt.Errorf("no topic named %q is deployed", name)
	}
	return topic, nil
}

func (q *Queue) queueOf(topicName, consumer string) (string, error) {
	topic, err := q.topic(topicName)
	if err != nil {
		return "", err
	}
	name, found := topic.Queues[consumer]
	if !found {
		return "", fmt.Errorf("topic %q has no queue for consumer %q", topicName, consumer)
	}
	return name, nil
}

func (q *Queue) consumerOnQueue(queue string) (string, string, bool) {
	for name, topic := range q.topology.Topics {
		if topic.Declared == nil {
			continue
		}
		for consumer, held := range topic.Queues {
			if held == queue {
				return name, consumer, true
			}
		}
	}
	return "", "", false
}

func (q *Queue) url(ctx context.Context, queue string) (string, error) {
	q.mu.Lock()
	url, found := q.urls[queue]
	q.mu.Unlock()
	if found {
		return url, nil
	}
	out, err := q.queues.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: aws.String(queue)})
	if err != nil {
		return "", fmt.Errorf("find queue %s: %w", queue, err)
	}
	q.mu.Lock()
	q.urls[queue] = aws.ToString(out.QueueUrl)
	q.mu.Unlock()
	return aws.ToString(out.QueueUrl), nil
}

func (q *Queue) Send(ctx context.Context, msg runs.Message) error {
	topic, err := q.topic(msg.Topic)
	if err != nil {
		return err
	}
	body, err := bodyOf(msg)
	if err != nil {
		return err
	}
	if msg.Consumer == "" {
		return q.publish(ctx, topic, msg, body)
	}
	return q.enqueue(ctx, topic, msg, body)
}

func (q *Queue) publish(ctx context.Context, topic queues.Topic, msg runs.Message, body string) error {
	if topic.SNS == "" {
		return fmt.Errorf("topic %q has no SNS topic to publish message %s to", msg.Topic, msg.ID)
	}
	in := &sns.PublishInput{TopicArn: aws.String(topic.SNS), Message: aws.String(body)}
	if topic.Declared.Ordered {
		in.MessageGroupId = aws.String(groupOf(msg, msg.ID))
		in.MessageDeduplicationId = aws.String(msg.ID)
	}
	if _, err := q.topics.Publish(ctx, in); err != nil {
		return fmt.Errorf("publish message %s to topic %s: %w", msg.ID, msg.Topic, err)
	}
	for _, consumer := range topic.Declared.Consumers {
		q.services.wake(ctx, consumer.Worker, time.Until(msg.DueAt))
	}
	return nil
}

func (q *Queue) enqueue(ctx context.Context, topic queues.Topic, msg runs.Message, body string) error {
	name, err := q.queueOf(msg.Topic, msg.Consumer)
	if err != nil {
		return err
	}
	url, err := q.url(ctx, name)
	if err != nil {
		return err
	}
	delay := time.Until(msg.DueAt)
	execution := executionOf(msg)
	in := &sqs.SendMessageInput{QueueUrl: aws.String(url), MessageBody: aws.String(body)}
	if queues.IsFIFO(name) {
		in.MessageGroupId = aws.String(groupOf(msg, execution))
		in.MessageDeduplicationId = aws.String(execution + "." + msg.Delivery)
	} else {
		in.DelaySeconds = ceilSeconds(min(delay, maxMessageDelay))
	}
	if _, err := q.queues.SendMessage(ctx, in); err != nil {
		return fmt.Errorf("send %s to queue %s: %w", execution, name, err)
	}
	if i := slices.IndexFunc(topic.Declared.Consumers, func(c provider.ConsumerSpec) bool { return c.Name == msg.Consumer }); i >= 0 {
		q.services.wake(ctx, topic.Declared.Consumers[i].Worker, delay)
	}
	return nil
}

func (q *Queue) Release(ctx context.Context, topic, consumer, handle string) error {
	name, err := q.queueOf(topic, consumer)
	if err != nil {
		return err
	}
	url, err := q.url(ctx, name)
	if err != nil {
		return err
	}
	_, err = q.queues.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(url), ReceiptHandle: aws.String(handle), VisibilityTimeout: 0})
	return err
}

type visibilityChange struct {
	handle  string
	seconds int32
}

func (q *Queue) changeVisibility(ctx context.Context, url string, changes []visibilityChange) {
	for chunk := range slices.Chunk(changes, maxSQSBatch) {
		entries := make([]sqstypes.ChangeMessageVisibilityBatchRequestEntry, 0, len(chunk))
		for i, change := range chunk {
			entries = append(entries, sqstypes.ChangeMessageVisibilityBatchRequestEntry{
				Id:                aws.String(strconv.Itoa(i)),
				ReceiptHandle:     aws.String(change.handle),
				VisibilityTimeout: change.seconds,
			})
		}
		out, err := q.queues.ChangeMessageVisibilityBatch(ctx, &sqs.ChangeMessageVisibilityBatchInput{QueueUrl: aws.String(url), Entries: entries})
		if err != nil {
			slog.Warn("change how long messages stay hidden", "error", err)
			continue
		}
		for _, failed := range out.Failed {
			slog.Warn("change how long a message stays hidden", "code", aws.ToString(failed.Code), "message", aws.ToString(failed.Message))
		}
	}
}

func sentAtOf(attributes map[string]string) time.Time {
	millis, err := strconv.ParseInt(attributes[string(sqstypes.MessageSystemAttributeNameSentTimestamp)], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(millis)
}

func outlivesRetention(queue string, sentAt time.Time, hidden time.Duration, now time.Time) bool {
	return !queues.IsFIFO(queue) && !sentAt.IsZero() && now.Add(hidden+retentionMargin).After(sentAt.Add(queues.MessageRetention))
}

func (q *Queue) sendAgain(ctx context.Context, url, body string, hidden time.Duration) error {
	_, err := q.queues.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(url), MessageBody: aws.String(body), DelaySeconds: ceilSeconds(min(hidden, maxMessageDelay))})
	return err
}

func visibilityOf(handle string, hidden time.Duration, receivedAt, now time.Time) (visibilityChange, bool) {
	remaining := receivedAt.Add(maxVisibility).Sub(now)
	if remaining < time.Second {
		return visibilityChange{}, false
	}
	return visibilityChange{handle: handle, seconds: min(ceilSeconds(hidden), int32(remaining/time.Second))}, true
}

func ceilSeconds(d time.Duration) int32 {
	if d <= 0 {
		return 0
	}
	return int32((d + time.Second - 1) / time.Second)
}
