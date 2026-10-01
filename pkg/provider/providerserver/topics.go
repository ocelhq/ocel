package providerserver

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/cron"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

var lanes = map[topicv1.Lane]provider.Lane{
	topicv1.Lane_LANE_HIGH:    provider.LaneHigh,
	topicv1.Lane_LANE_DEFAULT: provider.LaneDefault,
	topicv1.Lane_LANE_LOW:     provider.LaneLow,
}

func topicSpec(kind provider.BindingType, topic *contractv1.ManifestTopic) (*provider.TopicSpec, error) {
	spec := &provider.TopicSpec{
		Schema:  topic.GetSchema(),
		Ordered: topic.GetOrdered(),
		TTL:     topic.GetTtl().AsDuration(),
		Cron:    topic.GetCron(),
	}
	if spec.TTL < 0 {
		return nil, fmt.Errorf("its ttl %v is negative, and a message lives for more than no time", spec.TTL)
	}
	if spec.Cron != "" {
		if kind != provider.BindingTask {
			return nil, fmt.Errorf("it runs on a schedule, and only a task runs on one")
		}
		if _, err := cron.Parse(spec.Cron); err != nil {
			return nil, err
		}
	}
	seen := map[string]bool{}
	for _, message := range topic.GetConsumers() {
		consumer, err := consumerSpec(topic.GetRetry(), message)
		if err != nil {
			return nil, err
		}
		if seen[consumer.Name] {
			return nil, fmt.Errorf("it declares consumer %q twice, and a consumer owns one queue", consumer.Name)
		}
		seen[consumer.Name] = true
		if consumer.Exclusive && kind != provider.BindingTask {
			return nil, fmt.Errorf("consumer %q is exclusive, and only a task's own consumer is", consumer.Name)
		}
		spec.Consumers = append(spec.Consumers, consumer)
	}
	if kind == provider.BindingTask && (len(spec.Consumers) != 1 || !spec.Consumers[0].Exclusive) {
		return nil, fmt.Errorf("a task runs on one exclusive consumer, and it declares %d consumers", len(spec.Consumers))
	}
	return spec, nil
}

func consumerSpec(topicRetry *resourcesv1.RetryPolicy, message *contractv1.ManifestConsumer) (provider.ConsumerSpec, error) {
	if err := naming.Validate("consumer name", message.GetName()); err != nil {
		return provider.ConsumerSpec{}, err
	}
	if err := naming.Validate(fmt.Sprintf("the worker of consumer %q", message.GetName()), message.GetWorker()); err != nil {
		return provider.ConsumerSpec{}, err
	}
	consumer := provider.ConsumerSpec{
		Name:        message.GetName(),
		Worker:      message.GetWorker(),
		Exclusive:   message.GetExclusive(),
		Retry:       provider.ResolveRetryPolicy(topicRetry, message.GetRetry()),
		Concurrency: int(message.GetConcurrency()),
		MaxDuration: message.GetMaxDuration().AsDuration(),
	}
	if consumer.Concurrency < 0 {
		return provider.ConsumerSpec{}, fmt.Errorf("consumer %q has concurrency %d, and concurrency is never negative", consumer.Name, consumer.Concurrency)
	}
	if consumer.MaxDuration < 0 {
		return provider.ConsumerSpec{}, fmt.Errorf("consumer %q has maxDuration %v, and a run needs more than no time", consumer.Name, consumer.MaxDuration)
	}
	for _, lane := range message.GetLanes() {
		known, found := lanes[lane]
		if !found {
			return provider.ConsumerSpec{}, fmt.Errorf("consumer %q reads lane %s, and the lanes are high, default and low", consumer.Name, lane)
		}
		consumer.Lanes = append(consumer.Lanes, known)
	}
	if batch := message.GetBatch(); batch != nil {
		consumer.Batch = &provider.BatchPolicy{Size: int(batch.GetSize()), Timeout: batch.GetTimeout().AsDuration()}
		if consumer.Batch.Size < 1 || consumer.Batch.Timeout < 0 {
			return provider.ConsumerSpec{}, fmt.Errorf("consumer %q has batch size %d and timeout %v, and a batch holds at least one message and waits no negative time",
				consumer.Name, consumer.Batch.Size, consumer.Batch.Timeout)
		}
	}
	return consumer, nil
}
