package topic

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const (
	mockAccount       = "123456789012"
	testStateTableARN = "arn:aws:dynamodb:us-east-1:" + mockAccount + ":table/ocel-state"
)

func taskResource(name string, consumer provider.ConsumerSpec) provider.Resource {
	consumer.Name, consumer.Exclusive = name, true
	return provider.Resource{Name: "topic--" + name, Declared: name, Type: provider.BindingTask, Topic: &provider.TopicSpec{Consumers: []provider.ConsumerSpec{consumer}}}
}

func topicResource(name string, consumers ...provider.ConsumerSpec) provider.Resource {
	return provider.Resource{Name: "topic--" + name, Declared: name, Type: provider.BindingTopic, Topic: &provider.TopicSpec{Consumers: consumers}}
}

var shopNames = queues.Names{Namespace: "ocel", Project: "shop", Env: "prod"}

func mustOf(t *testing.T, resources []provider.Resource) Topics {
	t.Helper()
	topics, err := Of(shopNames, resources)
	if err != nil {
		t.Fatal(err)
	}
	return topics
}

func queueName(t *testing.T, topic, consumer string, fifo bool) string {
	t.Helper()
	named, err := shopNames.Queues(topic, consumer, fifo)
	if err != nil {
		t.Fatal(err)
	}
	return named.Queue
}

func topicName(t *testing.T, topic string, fifo bool) string {
	t.Helper()
	named, err := shopNames.Topic(topic, fifo)
	if err != nil {
		t.Fatal(err)
	}
	return named
}
