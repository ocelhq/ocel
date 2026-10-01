package manifest

import (
	"cmp"
	"slices"
	"strings"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/cron"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type Placement struct {
	Topics  map[string]*contractv1.ManifestTopic
	Workers []*contractv1.ManifestWorker
	Sources map[string][]string
}

func PlaceConsumers(cfg *project.Project, resources []declaration.Resource) (Placement, error) {
	declarations := declaredResources(cfg.Dir, resources)
	topics, workers, err := placeConsumers(appsOf(cfg.Dir, cfg.Apps, nil, nil), declarations, nil)
	if err != nil {
		return Placement{}, err
	}
	sources := map[string][]string{}
	for i, d := range declarations {
		if consumer := d.consumer(); consumer != nil {
			sources[consumer.GetWorker()] = append(sources[consumer.GetWorker()], resources[i].Source)
		}
	}
	return Placement{Topics: topics, Workers: workers, Sources: sources}, nil
}

func (p Placement) HostedWorkers() build.HostedWorkers {
	hosted := build.HostedWorkers{}
	for _, worker := range p.Workers {
		hosted[worker.GetApp()] = append(hosted[worker.GetApp()], p.Sources[worker.GetName()]...)
	}
	return hosted
}

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
		ordered := topic.Topic.GetOrdered()
		if ordered {
			if err := provider.RefuseOrderedConsumer(d.Consumer.GetLanes(), d.Consumer.GetBatch()); err != nil {
				return nil, refuse(d, "%v", err)
			}
		}
		if err := provider.RefuseConsumerCount(len(consumers[name])+1, ordered); err != nil {
			return nil, refuse(d, "is one consumer too many: %s %v", topic.label(), err)
		}
		consumers[name] = append(consumers[name], d)
	}
	return consumers, nil
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
	resolved := provider.ResolveRetryPolicy(declared, consumer.GetRetry())
	consumer.Retry = &resourcesv1.RetryPolicy{
		MaxAttempts: int32(resolved.MaxAttempts),
		MinDelay:    durationpb.New(resolved.MinDelay),
		MaxDelay:    durationpb.New(resolved.MaxDelay),
	}
	return consumer
}

func refuseTopicLimits(d declaredResource) error {
	return refuseAtDeclaration(d, provider.RefuseRetry(d.Topic.GetRetry()))
}

func refuseConsumerLimits(d declaredResource) error {
	return refuseRunLimits(d, topicConsumer(d))
}

func refuseTaskLimits(d declaredResource) error {
	task := d.Task
	return firstRefusal(
		refuseAtDeclaration(d, provider.RefuseRetry(task.GetRetry())),
		refuseRunLimits(d, taskConsumer(d)),
		refuseAtDeclaration(d, provider.RefuseTTL(task.GetTtl())),
		refuseAtDeclaration(d, provider.RefuseOrderedTask(task.GetOrdered(), task.GetBatch())),
		refuseSchedule(d, task.GetCron()),
	)
}

func refuseSchedule(d declaredResource, expr string) error {
	if expr == "" {
		return nil
	}
	if _, err := cron.Parse(expr); err != nil {
		return refuse(d, "has an invalid schedule: %v", err)
	}
	return nil
}

func refuseRunLimits(d declaredResource, runs *contractv1.ManifestConsumer) error {
	return firstRefusal(
		refuseAtDeclaration(d, provider.RefuseRetry(runs.GetRetry())),
		refuseAtDeclaration(d, provider.RefuseConcurrency(runs.GetConcurrency())),
		refuseAtDeclaration(d, provider.RefuseMaxDuration(runs.GetMaxDuration())),
		refuseAtDeclaration(d, provider.RefuseBatch(runs.GetBatch())),
	)
}
