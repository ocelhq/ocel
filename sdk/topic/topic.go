// Package topic declares topics: messages an app sends fan out to every
// consumer of the topic, each handling its own copy on a worker.
package topic

import (
	"context"
	"fmt"

	"ocel.dev/internal/binding"
	"ocel.dev/internal/codec"
	"ocel.dev/internal/declaration"
	"ocel.dev/internal/delivery"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
	"ocel.dev/internal/proto/app/topic/v1/topicv1connect"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
	"ocel.dev/runs"
)

// A Topic is a topic an app declares: messages sent to it fan out to every
// consumer it has, each handling its own copy.
type Topic[P any] struct {
	name       string
	connection binding.Connection[topicv1connect.TopicServiceClient]
}

// New declares a topic named name whose messages carry a P, encoded as JSON.
// Call it from a file under the project's discovery folder: during discovery
// the call is the declaration, and at runtime it reads the binding the deploy
// delivered for that name.
func New[P any](name string, opts ...Option) *Topic[P] {
	s := settings{config: &resourcesv1.TopicConfig{}}
	for _, opt := range opts {
		opt.applyTopic(&s)
	}
	declaration.Declare(declaration.CallSite(0), &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC, Name: name},
		Config:   &resourcesv1.DeclareRequest_Topic{Topic: s.config},
	})
	return &Topic[P]{name: name}
}

// Name is the name the topic was declared under.
func (t *Topic[P]) Name() string { return t.name }

// A Consumer is a consumer of a topic, declared with its Consumer or
// BatchConsumer.
type Consumer struct {
	topic string
	name  string
}

// Name is the name the consumer was declared under.
func (c *Consumer) Name() string { return c.name }

// Topic is the name of the topic the consumer reads.
func (c *Consumer) Topic() string { return c.topic }

// Consumer declares a consumer named name that handles every message sent to
// the topic, one at a time. An error handle returns fails the attempt, and it
// is retried unless the error wraps [ocel.dev/runs.ErrAbort].
func (t *Topic[P]) Consumer(name string, handle func(ctx context.Context, payload P) error, opts ...ConsumerOption) *Consumer {
	settings := t.newConsumerSettings(opts)
	if settings.config.Batch != nil {
		panic(fmt.Sprintf("ocel: consumer %q of topic %q takes one message at a time, so it batches nothing: declare it with BatchConsumer", name, t.name))
	}
	t.declareConsumer(declaration.CallSite(0), name, settings)
	delivery.RegisterRoute(t.name, name, &delivery.Route{
		Kind:   runs.KindConsumer,
		Name:   name,
		Worker: delivery.ResolveWorkerName(settings.config.GetWorker()),
		Serve: delivery.NewSingleServeFunc(delivery.Work[P, any]{Kind: runs.KindConsumer, Name: name, Run: func(ctx context.Context, payload P) (any, error) {
			return nil, handle(ctx, payload)
		}}),
	})
	return &Consumer{topic: t.name, name: name}
}

// BatchConsumer declares a consumer named name that handles the messages sent
// to the topic in batches of at most size, up to 1000, or 10 when the topic
// is ordered, and bounded in time by [BatchTimeout]. An error handle returns
// fails the whole batch.
func (t *Topic[P]) BatchConsumer(name string, size int, handle func(ctx context.Context, payloads []P) error, opts ...ConsumerOption) *Consumer {
	settings := t.newConsumerSettings(opts)
	delivery.EnsureBatchPolicy(&settings.config.Batch).Size = int32(size)
	t.declareConsumer(declaration.CallSite(0), name, settings)
	delivery.RegisterRoute(t.name, name, &delivery.Route{
		Kind:    runs.KindConsumer,
		Name:    name,
		Worker:  delivery.ResolveWorkerName(settings.config.GetWorker()),
		Batched: true,
		Serve: delivery.NewBatchServeFunc(delivery.Work[[]P, any]{Kind: runs.KindConsumer, Name: name, Run: func(ctx context.Context, payloads []P) (any, error) {
			return nil, handle(ctx, payloads)
		}}),
	})
	return &Consumer{topic: t.name, name: name}
}

func (t *Topic[P]) newConsumerSettings(opts []ConsumerOption) consumerSettings {
	settings := consumerSettings{config: &resourcesv1.ConsumerConfig{Topic: t.name}}
	for _, opt := range opts {
		opt.applyConsumer(&settings)
	}
	return settings
}

func (t *Topic[P]) declareConsumer(source, name string, settings consumerSettings) {
	declaration.Declare(source, &resourcesv1.DeclareRequest{
		Resource: &resourcesv1.ResourceIdentifier{Type: resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER, Name: name},
		Config:   &resourcesv1.DeclareRequest_Consumer{Consumer: settings.config},
	})
}

// Send publishes payload to the topic and returns the message's id.
func (t *Topic[P]) Send(ctx context.Context, payload P, opts ...SendOption) (string, error) {
	topic, err := t.connect("Send")
	if err != nil {
		return "", err
	}
	body, err := codec.EncodePayload(payload)
	if err != nil {
		return "", err
	}
	settings := sendSettings{}
	for _, opt := range opts {
		opt.applySend(&settings)
	}
	res, err := topic.Send(ctx, &topicv1.SendRequest{
		Topic:          t.name,
		Payload:        body,
		DueAt:          delivery.EncodeDueAt(settings.due),
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
func (t *Topic[P]) DeadLetter(consumer string) *DeadLetters {
	return &DeadLetters{topic: t.name, consumer: consumer, connect: t.connect}
}

func (t *Topic[P]) connect(access string) (topicv1connect.TopicServiceClient, error) {
	return t.connection.Connect(fmt.Sprintf("topic(%q)", t.name), access, func() (topicv1connect.TopicServiceClient, error) {
		return binding.Dial(t.name, bindingsv1.BindingType_BINDING_TYPE_TOPIC, topicv1connect.NewTopicServiceClient)
	})
}
