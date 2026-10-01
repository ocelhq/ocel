package ocel

import (
	"context"
	"fmt"

	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
	"ocel.dev/internal/proto/app/topic/v1/topicv1connect"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

// A TopicDefinition is a topic an app declares: messages sent to it fan out to
// every consumer it has, each handling its own copy.
type TopicDefinition[P any] struct {
	name       string
	connection boundResourceConnection[topicv1connect.TopicServiceClient]
}

// Topic declares a topic named name whose messages carry a P, encoded as JSON.
// Call it from a file under the project's discovery folder: during discovery
// the call is the declaration, and at runtime it reads the binding the deploy
// delivered for that name.
func Topic[P any](name string, opts ...TopicOption) *TopicDefinition[P] {
	settings := topicSettings{config: &resourcesv1.TopicConfig{}}
	for _, opt := range opts {
		opt.applyTopic(&settings)
	}
	declareResource(readCallSite(), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, Name: name},
		Config:   &resourcesv1.DeclareRequest_Topic{Topic: settings.config},
	})
	return &TopicDefinition[P]{name: name}
}

// Name is the name the topic was declared under.
func (t *TopicDefinition[P]) Name() string { return t.name }

// A ConsumerDefinition is a consumer of a topic, declared with its
// Consumer or BatchConsumer.
type ConsumerDefinition struct {
	topic string
	name  string
}

// Name is the name the consumer was declared under.
func (c *ConsumerDefinition) Name() string { return c.name }

// Topic is the name of the topic the consumer reads.
func (c *ConsumerDefinition) Topic() string { return c.topic }

// Consumer declares a consumer named name that handles every message sent to
// the topic, one at a time. An error handle returns fails the attempt, and it
// is retried unless the error wraps [ErrAbort].
func (t *TopicDefinition[P]) Consumer(name string, handle func(ctx context.Context, payload P) error, opts ...ConsumerOption) *ConsumerDefinition {
	settings := t.newConsumerSettings(opts)
	if settings.config.Batch != nil {
		panic(fmt.Sprintf("ocel: consumer %q of topic %q takes one message at a time, so it batches nothing: declare it with BatchConsumer", name, t.name))
	}
	t.declareConsumer(readCallSite(), name, settings)
	registerRoute(t.name, name, &route{
		kind:   KindConsumer,
		name:   name,
		worker: resolveWorkerName(settings.config.GetWorker()),
		serve: newSingleServeFunc(work[P, any]{kind: KindConsumer, name: name, run: func(ctx context.Context, payload P) (any, error) {
			return nil, handle(ctx, payload)
		}}),
	})
	return &ConsumerDefinition{topic: t.name, name: name}
}

// BatchConsumer declares a consumer named name that handles the messages sent
// to the topic in batches of at most size, up to 1000, or 10 when the topic
// is ordered, and bounded in time by [BatchTimeout]. An error handle returns
// fails the whole batch.
func (t *TopicDefinition[P]) BatchConsumer(name string, size int, handle func(ctx context.Context, payloads []P) error, opts ...ConsumerOption) *ConsumerDefinition {
	settings := t.newConsumerSettings(opts)
	ensureBatchPolicy(&settings.config.Batch).Size = int32(size)
	t.declareConsumer(readCallSite(), name, settings)
	registerRoute(t.name, name, &route{
		kind:    KindConsumer,
		name:    name,
		worker:  resolveWorkerName(settings.config.GetWorker()),
		batched: true,
		serve: newBatchServeFunc(work[[]P, any]{kind: KindConsumer, name: name, run: func(ctx context.Context, payloads []P) (any, error) {
			return nil, handle(ctx, payloads)
		}}),
	})
	return &ConsumerDefinition{topic: t.name, name: name}
}

func (t *TopicDefinition[P]) newConsumerSettings(opts []ConsumerOption) consumerSettings {
	settings := consumerSettings{config: &resourcesv1.ConsumerConfig{Topic: t.name}}
	for _, opt := range opts {
		opt.applyConsumer(&settings)
	}
	return settings
}

func (t *TopicDefinition[P]) declareConsumer(source, name string, settings consumerSettings) {
	declareResource(source, &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER, Name: name},
		Config:   &resourcesv1.DeclareRequest_Consumer{Consumer: settings.config},
	})
}

// Send publishes payload to the topic and returns the message's id.
func (t *TopicDefinition[P]) Send(ctx context.Context, payload P, opts ...SendOption) (string, error) {
	topic, err := t.connect("Send")
	if err != nil {
		return "", err
	}
	body, err := encodePayload(payload)
	if err != nil {
		return "", err
	}
	settings := sendSettings{}
	for _, opt := range opts {
		opt.applySend(&settings)
	}
	res, err := topic.client.Send(ctx, &topicv1.SendRequest{
		Topic:          topic.name,
		Payload:        body,
		DueAt:          settings.encodeDueAt(),
		IdempotencyKey: settings.idempotencyKey,
		Key:            settings.key,
		Lane:           settings.lane,
	})
	if err != nil {
		return "", err
	}
	return res.GetMessageId(), nil
}

// DeadLetter is the messages consumer gave up on, once they ran out of
// attempts or were aborted.
func (t *TopicDefinition[P]) DeadLetter(consumer string) *DeadLetters {
	return &DeadLetters{consumer: consumer, connect: t.connect}
}

func (t *TopicDefinition[P]) connect(access string) (*boundResource[topicv1connect.TopicServiceClient], error) {
	return t.connection.connect(fmt.Sprintf("topic(%q)", t.name), access, func() (*boundResource[topicv1connect.TopicServiceClient], error) {
		return dialBoundResource(t.name, bindingsv1.BindingType_BINDING_TYPE_TOPIC,
			func(delivered *bindingsv1.Binding) string { return delivered.GetTopic().GetTopic() },
			topicv1connect.NewTopicServiceClient)
	})
}
