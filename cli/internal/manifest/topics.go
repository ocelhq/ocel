package manifest

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ocelhq/ocel/pkg/cron"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	maxAttempts           = 100
	maxRetryDelay         = 600 * time.Second
	maxTTL                = 14 * 24 * time.Hour
	maxBatchSize          = 1000
	maxOrderedBatchSize   = 10
	maxBatchTimeout       = 300 * time.Second
	maxOrderedConsumers   = 100
	maxUnorderedConsumers = 1000
)

func placeConsumers(apps []app, declarations []declaredResource, ceilings []provider.WorkerCeiling) (map[string]*contractv1.ManifestTopic, []*contractv1.ManifestWorker, error) {
	topics := map[string]declaredResource{}
	declaredWorkers := map[string]declaredResource{}
	var served []declaredResource
	seen := map[namespacedName]declaredResource{}
	for _, d := range declarations {
		kind, found := kinds[d.Type]
		if !found {
			continue
		}
		if err := firstRefusal(refuseInvalidName(d, d.Name), kind.refuseLimits(d)); err != nil {
			return nil, nil, err
		}
		key := d.namespaced(kind)
		if prior, taken := seen[key]; taken {
			return nil, nil, refuseDuplicate(prior, d, key)
		}
		seen[key] = d
		if kind.declaresTopic {
			topics[d.Name] = d
		}
		if kind.consumer != nil {
			served = append(served, d)
		}
		if d.Type == resourcesv1.ResourceType_RESOURCE_TYPE_WORKER {
			declaredWorkers[d.Name] = d
		}
	}
	consumers, err := attachConsumers(topics, declarations)
	if err != nil {
		return nil, nil, err
	}
	workers, err := joinWorkers(apps, ceilings, served, declaredWorkers)
	if err != nil {
		return nil, nil, err
	}
	manifestTopics := make(map[string]*contractv1.ManifestTopic, len(topics))
	for name, d := range topics {
		manifestTopics[name] = manifestTopic(d, consumers[name])
	}
	return manifestTopics, workers, nil
}

func (d declaredResource) consumer() *contractv1.ManifestConsumer {
	if kind := kinds[d.Type]; kind.consumer != nil {
		return kind.consumer(d)
	}
	return nil
}

func taskConsumer(d declaredResource) *contractv1.ManifestConsumer {
	task := d.Task
	return &contractv1.ManifestConsumer{
		Name:        d.Name,
		Worker:      cmp.Or(task.GetWorker(), defaultWorker),
		Exclusive:   true,
		Concurrency: task.GetConcurrency(),
		MaxDuration: task.GetMaxDuration(),
		Batch:       task.GetBatch(),
	}
}

func topicConsumer(d declaredResource) *contractv1.ManifestConsumer {
	consumer := d.Consumer
	return &contractv1.ManifestConsumer{
		Name:        d.Name,
		Worker:      cmp.Or(consumer.GetWorker(), defaultWorker),
		Retry:       consumer.GetRetry(),
		Concurrency: consumer.GetConcurrency(),
		MaxDuration: consumer.GetMaxDuration(),
		Lanes:       consumer.GetLanes(),
		Batch:       consumer.GetBatch(),
	}
}

func attachConsumers(topics map[string]declaredResource, declarations []declaredResource) (map[string][]declaredResource, error) {
	consumers := map[string][]declaredResource{}
	for _, d := range declarations {
		if d.Consumer == nil {
			continue
		}
		name := d.Consumer.GetTopic()
		if err := refuseInvalidName(d, name); err != nil {
			return nil, err
		}
		topic, declared := topics[name]
		switch {
		case !declared:
			return nil, refuse(d, "consumes topic %q, and nothing declares it", name)
		case topic.Task != nil:
			return nil, refuse(d, "consumes %q, which is a task, and a task's one consumer is its own run", name)
		}
		limit := maxUnorderedConsumers
		if topic.Topic.GetOrdered() {
			limit = maxOrderedConsumers
			if size := d.Consumer.GetBatch().GetSize(); size > maxOrderedBatchSize {
				return nil, refuse(d, "has batch size %d, and a consumer of an ordered topic batches at most %d", size, maxOrderedBatchSize)
			}
			if len(d.Consumer.GetLanes()) > 0 {
				return nil, refuse(d, "reads lanes, and a consumer of an ordered topic reads each key in send order whatever its lane: drop lanes or ordered")
			}
		}
		if len(consumers[name]) == limit {
			return nil, refuse(d, "is one consumer too many: %s takes at most %d consumers%s", topic.label(), limit, orderedSuffix(topic.Topic.GetOrdered()))
		}
		consumers[name] = append(consumers[name], d)
	}
	return consumers, nil
}

func orderedSuffix(ordered bool) string {
	if ordered {
		return " when ordered"
	}
	return ""
}

func manifestTopic(d declaredResource, consumers []declaredResource) *contractv1.ManifestTopic {
	if d.Task != nil {
		task := d.Task
		return &contractv1.ManifestTopic{
			Schema:    task.GetSchema(),
			Ordered:   task.GetOrdered(),
			Retry:     task.GetRetry(),
			Ttl:       task.GetTtl(),
			Cron:      task.GetCron(),
			Consumers: []*contractv1.ManifestConsumer{withRetry(taskConsumer(d), task.GetRetry())},
		}
	}
	topic := &contractv1.ManifestTopic{Schema: d.Topic.GetSchema(), Ordered: d.Topic.GetOrdered(), Retry: d.Topic.GetRetry()}
	for _, c := range consumers {
		topic.Consumers = append(topic.Consumers, withRetry(topicConsumer(c), d.Topic.GetRetry()))
	}
	slices.SortFunc(topic.Consumers, func(a, b *contractv1.ManifestConsumer) int { return strings.Compare(a.GetName(), b.GetName()) })
	return topic
}

func withRetry(consumer *contractv1.ManifestConsumer, declared *resourcesv1.RetryPolicy) *contractv1.ManifestConsumer {
	resolved := &resourcesv1.RetryPolicy{
		MaxAttempts: provider.DefaultRetryMaxAttempts,
		MinDelay:    durationpb.New(provider.DefaultRetryMinDelay),
		MaxDelay:    durationpb.New(provider.DefaultRetryMaxDelay),
	}
	for _, retry := range []*resourcesv1.RetryPolicy{declared, consumer.GetRetry()} {
		if retry.GetMaxAttempts() > 0 {
			resolved.MaxAttempts = retry.GetMaxAttempts()
		}
		if retry.GetMinDelay() != nil {
			resolved.MinDelay = retry.GetMinDelay()
		}
		if retry.GetMaxDelay() != nil {
			resolved.MaxDelay = retry.GetMaxDelay()
		}
	}
	if resolved.GetMinDelay().AsDuration() > resolved.GetMaxDelay().AsDuration() {
		resolved.MaxDelay = resolved.GetMinDelay()
	}
	consumer.Retry = resolved
	return consumer
}

func refuseTopicLimits(d declaredResource) error {
	return refuseRetry(d, d.Topic.GetRetry())
}

func refuseConsumerLimits(d declaredResource) error {
	return refuseRunLimits(d, topicConsumer(d))
}

func refuseTaskLimits(d declaredResource) error {
	task := d.Task
	if err := firstRefusal(refuseRetry(d, task.GetRetry()), refuseRunLimits(d, taskConsumer(d))); err != nil {
		return err
	}
	if ttl := task.GetTtl(); ttl != nil && (ttl.AsDuration() <= 0 || ttl.AsDuration() > maxTTL) {
		return refuse(d, "has ttl %v, and a ttl is more than no time and at most %v (14 days)", ttl.AsDuration(), maxTTL)
	}
	if task.GetOrdered() && task.GetBatch() != nil {
		return refuse(d, "is ordered and batches, and an ordered task runs one run per key at a time: drop ordered or batch")
	}
	if expr := task.GetCron(); expr != "" {
		if _, err := cron.Parse(expr); err != nil {
			return refuse(d, "has an invalid schedule: %v", err)
		}
	}
	return nil
}

func refuseRunLimits(d declaredResource, runs *contractv1.ManifestConsumer) error {
	return firstRefusal(
		refuseRetry(d, runs.GetRetry()),
		refuseConcurrency(d, runs.GetConcurrency()),
		refuseMaxDuration(d, runs.GetMaxDuration()),
		refuseBatch(d, runs.GetBatch()),
	)
}

func refuseRetry(d declaredResource, retry *resourcesv1.RetryPolicy) error {
	if retry == nil {
		return nil
	}
	if attempts := retry.GetMaxAttempts(); attempts != 0 && (attempts < 1 || attempts > maxAttempts) {
		return refuse(d, "retries with maxAttempts %d, and maxAttempts is 1 to %d", attempts, maxAttempts)
	}
	minDelay, maxDelay := retry.GetMinDelay(), retry.GetMaxDelay()
	if minDelay != nil && minDelay.AsDuration() < 0 {
		return refuse(d, "retries with minDelay %v, and a delay is never negative", minDelay.AsDuration())
	}
	if maxDelay != nil && (maxDelay.AsDuration() < 0 || maxDelay.AsDuration() > maxRetryDelay) {
		return refuse(d, "retries with maxDelay %v, and maxDelay is at most %v", maxDelay.AsDuration(), maxRetryDelay)
	}
	if minDelay != nil && maxDelay != nil && minDelay.AsDuration() > maxDelay.AsDuration() {
		return refuse(d, "retries with minDelay %v above its maxDelay %v", minDelay.AsDuration(), maxDelay.AsDuration())
	}
	return nil
}

func refuseMaxDuration(d declaredResource, maxDuration *durationpb.Duration) error {
	if maxDuration != nil && maxDuration.AsDuration() <= 0 {
		return refuse(d, "has maxDuration %v, and a run needs more than no time", maxDuration.AsDuration())
	}
	return nil
}

func refuseBatch(d declaredResource, batch *resourcesv1.BatchPolicy) error {
	if batch == nil {
		return nil
	}
	if size := batch.GetSize(); size < 1 || size > maxBatchSize {
		return refuse(d, "has batch size %d, and a batch holds 1 to %d messages", size, maxBatchSize)
	}
	if timeout := batch.GetTimeout(); timeout != nil && (timeout.AsDuration() < 0 || timeout.AsDuration() > maxBatchTimeout) {
		return refuse(d, "has batch timeout %v, and a batch waits at most %v", timeout.AsDuration(), maxBatchTimeout)
	}
	return nil
}
