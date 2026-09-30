package manifest

import (
	"cmp"
	"slices"
	"strings"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

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

func (r *topicsAndWorkers) attachConsumers(topics map[string]declaredResource, declarations []declaredResource) error {
	for _, d := range declarations {
		if d.Consumer == nil {
			continue
		}
		name := d.Consumer.GetTopic()
		if err := refuseInvalidName(d, name); err != nil {
			return err
		}
		topic, declared := topics[name]
		switch {
		case !declared:
			return refuse(d, "consumes topic %q, and nothing declares it", name)
		case topic.Task != nil:
			return refuse(d, "consumes %q, which is a task, and a task's one consumer is its own run", name)
		}
		limit := maxUnorderedConsumers
		if topic.Topic.GetOrdered() {
			limit = maxOrderedConsumers
			if size := d.Consumer.GetBatch().GetSize(); size > maxOrderedBatchSize {
				return refuse(d, "has batch size %d, and a consumer of an ordered topic batches at most %d", size, maxOrderedBatchSize)
			}
		}
		if len(r.consumers[name]) == limit {
			return refuse(d, "is one consumer too many: %s takes at most %d consumers%s", topic.label(), limit, orderedNote(topic.Topic.GetOrdered()))
		}
		r.consumers[name] = append(r.consumers[name], d)
	}
	return nil
}

func orderedNote(ordered bool) string {
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
			Consumers: []*contractv1.ManifestConsumer{taskConsumer(d)},
		}
	}
	topic := &contractv1.ManifestTopic{Schema: d.Topic.GetSchema(), Ordered: d.Topic.GetOrdered(), Retry: d.Topic.GetRetry()}
	for _, c := range consumers {
		topic.Consumers = append(topic.Consumers, topicConsumer(c))
	}
	slices.SortFunc(topic.Consumers, func(a, b *contractv1.ManifestConsumer) int { return strings.Compare(a.GetName(), b.GetName()) })
	return topic
}
