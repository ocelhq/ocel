package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

const (
	maxMessageDelay    = 15 * time.Minute
	maxVisibilityDelay = 12 * time.Hour
)

type queueMessage struct {
	Execution   string          `json:"execution,omitempty"`
	Delivery    string          `json:"delivery,omitempty"`
	Message     string          `json:"message,omitempty"`
	PublishedAt int64           `json:"publishedAt,omitempty"`
	DueAt       int64           `json:"dueAt,omitempty"`
	Key         string          `json:"key,omitempty"`
	Lane        string          `json:"lane,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

func (m queueMessage) executionFor(consumer string) string {
	if m.Execution != "" {
		return m.Execution
	}
	return m.Message + "-" + consumer
}

func groupOf(key, execution string) string {
	if key != "" {
		return key
	}
	return execution
}

func (e *Engine) enqueue(ctx context.Context, deployed deployedConsumer, msg queueMessage, delay time.Duration) error {
	url, err := e.queueURL(ctx, deployed.queue)
	if err != nil {
		return err
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	in := &sqs.SendMessageInput{QueueUrl: aws.String(url), MessageBody: aws.String(string(body))}
	if deployed.fifo() {
		execution := msg.executionFor(deployed.consumer.Name)
		in.MessageGroupId = aws.String(groupOf(msg.Key, execution))
		in.MessageDeduplicationId = aws.String(execution + "." + msg.Delivery)
	} else if seconds := delaySeconds(delay, maxMessageDelay); seconds > 0 {
		in.DelaySeconds = seconds
	}
	if _, err := e.cfg.Queues.SendMessage(ctx, in); err != nil {
		return fmt.Errorf("send %s to queue %s: %w", msg.executionFor(deployed.consumer.Name), deployed.queue, err)
	}
	return nil
}

func (e *Engine) publish(ctx context.Context, topicName, topicARN string, fifo bool, msg queueMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	in := &sns.PublishInput{TopicArn: aws.String(topicARN), Message: aws.String(string(body))}
	if fifo {
		in.MessageGroupId = aws.String(groupOf(msg.Key, msg.Message))
		in.MessageDeduplicationId = aws.String(msg.Message)
	}
	if _, err := e.cfg.Topics.Publish(ctx, in); err != nil {
		return fmt.Errorf("publish message %s to topic %s: %w", msg.Message, topicName, err)
	}
	return nil
}

func delaySeconds(delay, ceiling time.Duration) int32 {
	if delay <= 0 {
		return 0
	}
	delay = min(delay, ceiling)
	seconds := int32((delay + time.Second - 1) / time.Second)
	return seconds
}
