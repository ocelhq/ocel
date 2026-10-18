package topic

import "github.com/ocelhq/ocel/pkg/provider"

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
