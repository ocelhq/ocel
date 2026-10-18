package deploy

import (
	"slices"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	pulumi "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const testStateTableARN = "arn:aws:dynamodb:us-east-1:" + mockAccount + ":table/ocel-state"

func taskResource(name string, consumer provider.ConsumerSpec, options ...func(*provider.TopicSpec)) provider.Resource {
	consumer.Name, consumer.Exclusive = name, true
	topic := &provider.TopicSpec{Consumers: []provider.ConsumerSpec{consumer}}
	for _, option := range options {
		option(topic)
	}
	return provider.Resource{Name: "topic--" + name, Declared: name, Type: provider.BindingTask, Topic: topic}
}

func topicResource(name string, consumers ...provider.ConsumerSpec) provider.Resource {
	return provider.Resource{Name: "topic--" + name, Declared: name, Type: provider.BindingTopic, Topic: &provider.TopicSpec{Consumers: consumers}}
}

func orderedTopic(topic *provider.TopicSpec) { topic.Ordered = true }

func cronEveryMinute(topic *provider.TopicSpec) { topic.Cron = "* * * * *" }

func TestATaskGetsAQueueAndDeadLetterQueueAndATopicFansOutThroughSNSToOneQueuePerConsumer(t *testing.T) {
	t.Parallel()

	resources := []provider.Resource{
		taskResource("resize", provider.ConsumerSpec{Worker: "worker", MaxDuration: time.Minute}),
		taskResource("sequence", provider.ConsumerSpec{Worker: "worker"}, orderedTopic),
		topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}, provider.ConsumerSpec{Name: "ledger-entry", Worker: "ledger", MaxDuration: time.Minute}),
		taskResource("rollup", provider.ConsumerSpec{Worker: "rollup", MaxDuration: time.Minute, Batch: &provider.BatchPolicy{Size: 50, Timeout: 20 * time.Second}}),
	}
	rec := &inputRecorder{}
	release := &release{cfg: Config{Region: "us-east-1", StateTableARN: testStateTableARN}}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error {
		return declareTopicsAndTasks(pctx, release, "shop", "prod", resources)
	}, pulumi.WithMocks("shop", "prod--infra", rec)); err != nil {
		t.Fatalf("declare the topics: %v", err)
	}

	queuesMade := map[string]resource.PropertyMap{}
	for _, name := range rec.registered("aws:sqs/queue:Queue") {
		inputs := rec.inputs("aws:sqs/queue:Queue", name)
		queuesMade[inputs["name"].StringValue()] = inputs
	}
	for _, consumer := range []struct {
		topic, consumer string
		fifo            bool
		visibility      float64
	}{
		{"resize", "resize", false, 5400},
		{"sequence", "sequence", true, 5400},
		{"orders", "audit-log", false, 5400},
		{"orders", "ledger-entry", false, 540},
		{"rollup", "rollup", false, 560},
	} {
		queue, made := queuesMade[queues.QueueName("shop", "prod", consumer.topic, consumer.consumer, consumer.fifo)]
		if !made {
			t.Errorf("no queue for %s's consumer %s among %v", consumer.topic, consumer.consumer, slices.Collect(mapKeys(queuesMade)))
			continue
		}
		if queue["fifoQueue"].BoolValue() != consumer.fifo || queue["visibilityTimeoutSeconds"].NumberValue() != consumer.visibility {
			t.Errorf("%s's queue = fifo %v visibility %v, want fifo %v and %vs: AWS asks an event source mapping's queue for six times its function's timeout plus its batching window", consumer.consumer, queue["fifoQueue"], queue["visibilityTimeoutSeconds"], consumer.fifo, consumer.visibility)
		}
		if _, dead := queuesMade[queues.DeadLetterQueueName("shop", "prod", consumer.topic, consumer.consumer, consumer.fifo)]; !dead {
			t.Errorf("no dead-letter queue for %s's consumer %s", consumer.topic, consumer.consumer)
		}
	}
	topicsMade := rec.registered("aws:sns/topic:Topic")
	if len(topicsMade) != 1 {
		t.Fatalf("SNS topics = %v, want only orders: a task is sent straight to its queue", topicsMade)
	}
	subscriptions := rec.registered("aws:sns/topicSubscription:TopicSubscription")
	if len(subscriptions) != 2 {
		t.Fatalf("subscriptions = %v, want one per consumer of orders", subscriptions)
	}
	for _, name := range subscriptions {
		if inputs := rec.inputs("aws:sns/topicSubscription:TopicSubscription", name); !inputs["rawMessageDelivery"].BoolValue() || inputs["protocol"].StringValue() != "sqs" {
			t.Errorf("subscription %s = %v, want raw delivery to sqs", name, inputs)
		}
	}
	if policies := rec.registered("aws:sqs/queuePolicy:QueuePolicy"); len(policies) != 2 {
		t.Errorf("queue policies = %v, want one letting SNS deliver to each consumer's queue", policies)
	}
	if groups := rec.registered("aws:scheduler/scheduleGroup:ScheduleGroup"); len(groups) != 0 {
		t.Errorf("schedule groups = %v, want none without a cron task", groups)
	}
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for key := range m {
			if !yield(key) {
				return
			}
		}
	}
}
