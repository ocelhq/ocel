package tasks

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

type queueService interface {
	GetQueueUrl(ctx context.Context, in *sqs.GetQueueUrlInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	SendMessage(ctx context.Context, in *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, optFns ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

type topicService interface {
	Publish(ctx context.Context, in *sns.PublishInput, optFns ...func(*sns.Options)) (*sns.PublishOutput, error)
}

type Config struct {
	Manifest queues.Manifest
	Table    table
	Queues   queueService
	Topics   topicService

	Worker    string
	WorkerURL string
	Client    *http.Client
}

type Engine struct {
	cfg   Config
	store store

	mu   sync.Mutex
	urls map[string]string
}

func New(cfg Config) *Engine {
	if cfg.Client == nil {
		cfg.Client = http.DefaultClient
	}
	var topics []string
	for name, topic := range cfg.Manifest.Topics {
		if topic.Declared != nil {
			topics = append(topics, name)
		}
	}
	return &Engine{
		cfg:   cfg,
		store: store{db: cfg.Table, table: cfg.Manifest.Table, prefix: cfg.Manifest.KeyPrefix, topics: topics},
		urls:  map[string]string{},
	}
}

type deployedConsumer struct {
	topicName string
	topic     *provider.TopicSpec
	consumer  provider.ConsumerSpec
	queue     string
}

func (d deployedConsumer) fifo() bool { return queues.IsFIFO(d.queue) }

func (d deployedConsumer) isTask() bool { return runs.IsTask(d.topic) }

func (d deployedConsumer) retry() provider.RetryPolicy {
	return d.consumer.Retry
}

func (e *Engine) topic(name string) (queues.Topic, bool) {
	topic, found := e.cfg.Manifest.Topics[name]
	return topic, found && topic.Declared != nil
}

func (e *Engine) consumer(topicName, consumerName string) (deployedConsumer, bool) {
	topic, found := e.topic(topicName)
	if !found {
		return deployedConsumer{}, false
	}
	for _, consumer := range topic.Declared.Consumers {
		if consumer.Name == consumerName {
			return deployedConsumer{topicName: topicName, topic: topic.Declared, consumer: consumer, queue: topic.Queues[consumerName]}, true
		}
	}
	return deployedConsumer{}, false
}

func (e *Engine) consumerOnQueue(queue string) (deployedConsumer, bool) {
	for name, topic := range e.cfg.Manifest.Topics {
		for consumer, held := range topic.Queues {
			if held == queue {
				return e.consumer(name, consumer)
			}
		}
	}
	return deployedConsumer{}, false
}

func (e *Engine) taskConsumer(name string) (deployedConsumer, bool) {
	topic, found := e.topic(name)
	if !found || !runs.IsTask(topic.Declared) {
		return deployedConsumer{}, false
	}
	return e.consumer(name, topic.Declared.Consumers[0].Name)
}

func (e *Engine) queueURL(ctx context.Context, queue string) (string, error) {
	e.mu.Lock()
	url, found := e.urls[queue]
	e.mu.Unlock()
	if found {
		return url, nil
	}
	out, err := e.cfg.Queues.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: &queue})
	if err != nil {
		return "", fmt.Errorf("find queue %s: %w", queue, err)
	}
	e.mu.Lock()
	e.urls[queue] = *out.QueueUrl
	e.mu.Unlock()
	return *out.QueueUrl, nil
}
