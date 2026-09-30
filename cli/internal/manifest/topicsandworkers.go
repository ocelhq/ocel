package manifest

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

type declarationKind struct {
	namespace     string
	declaresTopic bool
	consumer      func(d declaredResource) *contractv1.ManifestConsumer
	refuseLimits  func(d declaredResource) error
}

var topicTaskAndWorkerKinds = map[resourcesv1.ResourceType]declarationKind{
	resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC: {
		namespace: "topic", declaresTopic: true, refuseLimits: refuseTopicLimits,
	},
	resourcesv1.ResourceType_RESOURCE_TYPE_TASK: {
		namespace: "topic", declaresTopic: true, consumer: taskConsumer, refuseLimits: refuseTaskLimits,
	},
	resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER: {
		namespace: "consumer", consumer: topicConsumer, refuseLimits: refuseConsumerLimits,
	},
	resourcesv1.ResourceType_RESOURCE_TYPE_WORKER: {
		namespace: "worker", refuseLimits: refuseWorkerLimits,
	},
}

type namespacedName struct {
	namespace string
	name      string
}

func (d declaredResource) label() string {
	if d.Type == resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER {
		return fmt.Sprintf("consumer %q of topic %q", d.Name, d.Consumer.GetTopic())
	}
	return fmt.Sprintf("%s %q", naming.ResourceTypeName(d.Type), d.Name)
}

func (d declaredResource) namespaced(kind declarationKind) namespacedName {
	if d.Type == resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER {
		return namespacedName{kind.namespace, d.Consumer.GetTopic() + "/" + d.Name}
	}
	return namespacedName{kind.namespace, d.Name}
}

func (d declaredResource) consumer() *contractv1.ManifestConsumer {
	if kind := topicTaskAndWorkerKinds[d.Type]; kind.consumer != nil {
		return kind.consumer(d)
	}
	return nil
}

type topicsAndWorkers struct {
	topics    map[string]*contractv1.ManifestTopic
	workers   []*contractv1.ManifestWorker
	served    []declaredResource
	declared  map[string]declaredResource
	consumers map[string][]declaredResource
}

func resolveTopicsAndWorkers(apps []app, declarations []declaredResource, ceilings []provider.WorkerCeiling) (topicsAndWorkers, error) {
	resolved := topicsAndWorkers{
		topics:    map[string]*contractv1.ManifestTopic{},
		declared:  map[string]declaredResource{},
		consumers: map[string][]declaredResource{},
	}
	topics := map[string]declaredResource{}
	seen := map[namespacedName]declaredResource{}
	for _, d := range declarations {
		kind, found := topicTaskAndWorkerKinds[d.Type]
		if !found {
			continue
		}
		if err := firstRefusal(refuseInvalidName(d, d.Name), kind.refuseLimits(d)); err != nil {
			return topicsAndWorkers{}, err
		}
		key := d.namespaced(kind)
		if prior, taken := seen[key]; taken {
			return topicsAndWorkers{}, refuseDuplicate(prior, d, key)
		}
		seen[key] = d
		if kind.declaresTopic {
			topics[d.Name] = d
		}
		if kind.consumer != nil {
			resolved.served = append(resolved.served, d)
		}
		if d.Type == resourcesv1.ResourceType_RESOURCE_TYPE_WORKER {
			resolved.declared[d.Name] = d
		}
	}
	if err := resolved.attachConsumers(topics, declarations); err != nil {
		return topicsAndWorkers{}, err
	}
	if err := resolved.joinWorkers(apps, ceilings); err != nil {
		return topicsAndWorkers{}, err
	}
	for name, d := range topics {
		resolved.topics[name] = manifestTopic(d, resolved.consumers[name])
	}
	return resolved, nil
}

func refuseDuplicate(prior, d declaredResource, key namespacedName) error {
	if first, second := prior.consumer(), d.consumer(); first != nil && prior.Type == d.Type && first.GetWorker() != second.GetWorker() {
		return &ServedTwiceError{
			Subject:      d.label(),
			FirstSource:  prior.Source,
			FirstWorker:  first.GetWorker(),
			SecondSource: d.Source,
			SecondWorker: second.GetWorker(),
		}
	}
	return &DuplicateError{TypeToken: key.namespace, Name: key.name, FirstSource: prior.Source, SecondSource: d.Source}
}
