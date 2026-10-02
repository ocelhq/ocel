package providerserver

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/cron"
	"github.com/ocelhq/ocel/pkg/naming"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

var lanes = map[topicv1.Lane]provider.Lane{
	topicv1.Lane_LANE_HIGH:    provider.LaneHigh,
	topicv1.Lane_LANE_DEFAULT: provider.LaneDefault,
	topicv1.Lane_LANE_LOW:     provider.LaneLow,
}

func readTopicSpec(kind provider.BindingType, topic *contractv1.ManifestTopic) (*provider.TopicSpec, error) {
	if err := refuseTopicLimits(kind, topic); err != nil {
		return nil, fmt.Errorf("it %w", err)
	}
	spec := &provider.TopicSpec{
		Schema:  topic.GetSchema(),
		Ordered: topic.GetOrdered(),
		TTL:     topic.GetTtl().AsDuration(),
		Cron:    topic.GetCron(),
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
		consumer, err := readConsumerSpec(topic, message)
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

func refuseTopicLimits(kind provider.BindingType, topic *contractv1.ManifestTopic) error {
	if err := provider.RefuseRetry(topic.GetRetry()); err != nil {
		return err
	}
	if err := provider.RefuseTTL(topic.GetTtl()); err != nil {
		return err
	}
	if kind == provider.BindingTask {
		for _, consumer := range topic.GetConsumers() {
			if err := provider.RefuseOrderedTask(topic.GetOrdered(), consumer.GetBatch()); err != nil {
				return err
			}
		}
	}
	return provider.RefuseConsumerCount(len(topic.GetConsumers()), topic.GetOrdered())
}

func readConsumerSpec(topic *contractv1.ManifestTopic, message *contractv1.ManifestConsumer) (provider.ConsumerSpec, error) {
	if err := naming.Validate("consumer name", message.GetName()); err != nil {
		return provider.ConsumerSpec{}, err
	}
	if err := naming.Validate(fmt.Sprintf("the worker of consumer %q", message.GetName()), message.GetWorker()); err != nil {
		return provider.ConsumerSpec{}, err
	}
	if err := refuseConsumerLimits(topic.GetOrdered(), message); err != nil {
		return provider.ConsumerSpec{}, fmt.Errorf("consumer %q %w", message.GetName(), err)
	}
	consumer := provider.ConsumerSpec{
		Name:        message.GetName(),
		Worker:      message.GetWorker(),
		Exclusive:   message.GetExclusive(),
		Retry:       provider.ResolveRetryPolicy(topic.GetRetry(), message.GetRetry()),
		Concurrency: int(message.GetConcurrency()),
		MaxDuration: message.GetMaxDuration().AsDuration(),
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
	}
	return consumer, nil
}

func refuseConsumerLimits(ordered bool, message *contractv1.ManifestConsumer) error {
	refusals := []error{
		provider.RefuseRetry(message.GetRetry()),
		provider.RefuseConcurrency(message.GetConcurrency()),
		provider.RefuseMaxDuration(message.GetMaxDuration()),
		provider.RefuseBatch(message.GetBatch()),
	}
	if ordered {
		refusals = append(refusals, provider.RefuseOrderedConsumer(message.GetLanes(), message.GetBatch()))
	}
	for _, err := range refusals {
		if err != nil {
			return err
		}
	}
	return nil
}
