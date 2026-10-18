package pgmq

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/durationpb"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs/runsconformance"
)

var wireLanes = map[provider.Lane]topicv1.Lane{
	provider.LaneHigh:    topicv1.Lane_LANE_HIGH,
	provider.LaneDefault: topicv1.Lane_LANE_DEFAULT,
	provider.LaneLow:     topicv1.Lane_LANE_LOW,
}

func manifestTopicOf(spec *provider.TopicSpec) *contractv1.ManifestTopic {
	topic := &contractv1.ManifestTopic{Schema: spec.Schema, Ordered: spec.Ordered, Cron: spec.Cron}
	if spec.TTL > 0 {
		topic.Ttl = durationpb.New(spec.TTL)
	}
	for _, consumer := range spec.Consumers {
		declared := &contractv1.ManifestConsumer{
			Name:        consumer.Name,
			Worker:      consumer.Worker,
			Exclusive:   consumer.Exclusive,
			Concurrency: int32(consumer.Concurrency),
			Retry: &resourcesv1.RetryPolicy{
				MaxAttempts: int32(consumer.Retry.MaxAttempts),
				MinDelay:    durationpb.New(consumer.Retry.MinDelay),
				MaxDelay:    durationpb.New(consumer.Retry.MaxDelay),
			},
		}
		if consumer.MaxDuration > 0 {
			declared.MaxDuration = durationpb.New(consumer.MaxDuration)
		}
		for _, lane := range consumer.Lanes {
			declared.Lanes = append(declared.Lanes, wireLanes[lane])
		}
		if consumer.Batch != nil {
			declared.Batch = &resourcesv1.BatchPolicy{Size: int32(consumer.Batch.Size), Timeout: durationpb.New(consumer.Batch.Timeout)}
		}
		topic.Consumers = append(topic.Consumers, declared)
	}
	return topic
}

func TestTheEngineOverPgmqKeepsEveryRunSemantic(t *testing.T) {
	runsconformance.Engine(t, func(t *testing.T, topics map[string]*provider.TopicSpec) runsconformance.Wiring {
		engine := anEngine(t)
		deployment := Deployment{Slug: "app", Topics: map[string]*contractv1.ManifestTopic{}}
		for name, spec := range topics {
			deployment.Topics[name] = manifestTopicOf(spec)
		}
		if err := engine.Apply(context.Background(), deployment); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		return runsconformance.Wiring{Store: engine.Store(), Queue: queue{engine: engine}, Receiver: queue{engine: engine}}
	})
}
