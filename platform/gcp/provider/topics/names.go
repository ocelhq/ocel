package topics

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	maxPubSubName   = 255
	maxConsumerName = 63
)

var longestConsumer = strings.Repeat("c", maxConsumerName)

type Names struct {
	Namespace provider.Namespace
	Scope     Scope
}

func (n Names) Topic(topic string) (string, error) {
	coordinate := naming.Coordinate{Project: n.Scope.Slug, Env: n.Scope.Environment, App: naming.InfraApp, Kind: naming.KindTopic, Name: topic}
	return coordinate.AppResourceName(string(n.Namespace), coordinate.HashTag(string(n.Namespace))).Fit(maxPubSubName, longestConsumer)
}

func CheckConsumer(topic, consumer string) error {
	if len(consumer) > maxConsumerName {
		return refusal.Refuse(refusal.CodeInvalid,
			"consumer %s of %s is %d characters, and on Google Cloud a consumer takes %d: its Pub/Sub subscription is the topic's name and the consumer's, within the %d characters Pub/Sub takes.\nShorten %s",
			consumer, topic, len(consumer), maxConsumerName, maxPubSubName, consumer)
	}
	return nil
}

func (n Names) Subscription(topic, consumer string) (string, error) {
	if err := CheckConsumer(topic, consumer); err != nil {
		return "", err
	}
	name, err := n.Topic(topic)
	if err != nil {
		return "", err
	}
	return name + naming.WordSeparator + consumer, nil
}

func (n Names) topicPath(project, topic string) (string, error) {
	name, err := n.Topic(topic)
	if err != nil {
		return "", err
	}
	return TopicPath(project, name), nil
}

func (n Names) subscriptionPath(project, topic, consumer string) (string, error) {
	name, err := n.Subscription(topic, consumer)
	if err != nil {
		return "", err
	}
	return "projects/" + project + "/subscriptions/" + name, nil
}
