package topics

import (
	"maps"
	"math"
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const maxPubSubName = 255

type Names struct {
	Namespace provider.Namespace
	Scope     Scope
}

func (n Names) topicName(topic string) naming.PhysicalName {
	tag := naming.Coordinate{Project: n.Scope.Slug, Env: n.Scope.Environment, App: naming.InfraApp, Kind: naming.KindTopic, Name: topic}.HashTag(string(n.Namespace))
	return naming.PhysicalName{Fence: naming.AppFence(string(n.Namespace)), Project: n.Scope.Slug, Env: n.Scope.Environment, Declared: topic, Tag: tag}
}

func (n Names) Topic(topic string) string {
	name, err := n.topicName(topic).Fit(maxPubSubName)
	if err != nil {
		name, _ = n.topicName(topic).Fit(math.MaxInt)
	}
	return name
}

func (n Names) Subscription(topic, consumer string) string {
	return n.Topic(topic) + naming.WordSeparator + consumer
}

func (n Names) ScheduleJob(task string) string { return n.Topic(task) }

func (n Names) RefuseUnfit(declared map[string]*provider.TopicSpec) error {
	for _, topic := range slices.Sorted(maps.Keys(declared)) {
		if _, err := n.topicName(topic).Fit(maxPubSubName); err != nil {
			return refusal.Refuse(refusal.CodeInvalid, "topic %s would be a Pub/Sub topic, which takes %d characters: %v.\nName a shorter topic", topic, maxPubSubName, err)
		}
		for _, consumer := range declared[topic].Consumers {
			if subscription := n.Subscription(topic, consumer.Name); len(subscription) > maxPubSubName {
				return refusal.Refuse(refusal.CodeInvalid,
					"consumer %s of topic %s would be the Pub/Sub subscription %s, %d characters, and Pub/Sub takes %d: a subscription is its topic's name and the consumer's.\nName a shorter consumer or topic",
					consumer.Name, topic, subscription, len(subscription), maxPubSubName)
			}
		}
	}
	return nil
}
