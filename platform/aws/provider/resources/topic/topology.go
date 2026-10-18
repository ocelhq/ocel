package topic

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	sns "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sns"
	sqs "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sqs"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const (
	workerCeiling          = 15 * time.Minute
	containerWorkerCeiling = 12 * time.Hour
	workerSettleMargin     = 30 * time.Second

	eventSourceVisibilityFactor = 6
	snsServicePrincipal         = "sns.amazonaws.com"
	outputKeyTopicQueues        = "queues"
	outputKeyTopicARN           = "topic"
)

var WorkerCeilings = []provider.WorkerCeiling{
	{Compute: provider.ComputeServerless, MaxDuration: workerCeiling},
	{Compute: provider.ComputeContainer, MaxDuration: containerWorkerCeiling},
}

type Queue struct {
	Topic      string
	Consumer   provider.ConsumerSpec
	Name       string
	DeadLetter string
	FIFO       bool
}

type Topic struct {
	Resource provider.Resource
	Spec     *provider.TopicSpec
	SNS      string
	Queues   []Queue
}

func (t Topic) IsTask() bool { return t.Resource.Type == provider.BindingTask }

func Of(project, env string, resources []provider.Resource) Topics {
	var topics []Topic
	for _, resource := range resources {
		if resource.Topic == nil || resource.Binding != "" {
			continue
		}
		declared := resource.Topic
		fifo := declared.Ordered
		topic := Topic{Resource: resource, Spec: declared}
		if resource.Type != provider.BindingTask {
			topic.SNS = queues.TopicName(project, env, resource.Declared, fifo)
		}
		for _, consumer := range declared.Consumers {
			topic.Queues = append(topic.Queues, Queue{
				Topic:      resource.Declared,
				Consumer:   consumer,
				Name:       queues.QueueName(project, env, resource.Declared, consumer.Name, fifo),
				DeadLetter: queues.DeadLetterQueueName(project, env, resource.Declared, consumer.Name, fifo),
				FIFO:       fifo,
			})
		}
		topics = append(topics, topic)
	}
	slices.SortFunc(topics, func(a, b Topic) int { return cmp.Compare(a.Resource.Declared, b.Resource.Declared) })
	return topics
}

func WorkerTimeout(topics []Topic, worker string) time.Duration {
	longest := time.Duration(0)
	for _, topic := range topics {
		for _, queue := range topic.Queues {
			if queue.Consumer.Worker != worker {
				continue
			}
			maxDuration := queue.Consumer.MaxDuration
			if maxDuration <= 0 {
				return workerCeiling
			}
			longest = max(longest, maxDuration)
		}
	}
	if longest == 0 {
		return workerCeiling
	}
	return min(longest+workerSettleMargin, workerCeiling)
}

func VisibilitySeconds(topics []Topic, queue Queue) int {
	return eventSourceVisibilityFactor*int(WorkerTimeout(topics, queue.Consumer.Worker)/time.Second) + BatchingWindowSeconds(queue)
}

func SNSTopicARN(region, account, name string) string {
	return fmt.Sprintf("arn:aws:sns:%s:%s:%s", region, account, name)
}

func SQSQueueARN(region, account, name string) string {
	return fmt.Sprintf("arn:aws:sqs:%s:%s:%s", region, account, name)
}

type Topics []Topic

func (topics Topics) Served(worker string) []Queue {
	var served []Queue
	for _, topic := range topics {
		for _, queue := range topic.Queues {
			if queue.Consumer.Worker == worker {
				served = append(served, queue)
			}
		}
	}
	return served
}

func (topics Topics) CronTasks(worker string) []Topic {
	var tasks []Topic
	for _, topic := range topics {
		if topic.IsTask() && topic.Spec.Cron != "" && topic.Queues[0].Consumer.Worker == worker {
			tasks = append(tasks, topic)
		}
	}
	return tasks
}

func QueueTopology(table StateTable, project, env string, topics []Topic, workers map[string]queues.Worker) (queues.Topology, error) {
	topology := queues.Topology{Table: table.Name, KeyPrefix: naming.TaskKeyPrefix(project, env), Topics: map[string]queues.Topic{}, Workers: workers}
	if len(topics) == 0 {
		return topology, nil
	}
	account := component.AccountOfARN(table.ARN)
	if account == "" {
		return queues.Topology{}, fmt.Errorf("the state table ARN %q names no account, so the topics' ARNs cannot be written", table.ARN)
	}
	for _, topic := range topics {
		entry := queues.Topic{Declared: topic.Spec, Queues: map[string]string{}}
		if topic.SNS != "" {
			entry.SNS = SNSTopicARN(table.Region, account, topic.SNS)
		}
		for _, queue := range topic.Queues {
			entry.Queues[queue.Consumer.Name] = queue.Name
		}
		topology.Topics[topic.Resource.Declared] = entry
	}
	return topology, nil
}

func Register(ctx *pulumi.Context, topic Topic, visibility func(queue Queue) int, tags pulumi.StringMap) error {
	at := topic.Resource.Name
	var snsTopic *sns.Topic
	if topic.SNS != "" {
		var err error
		snsTopic, err = sns.NewTopic(ctx, naming.ResourceID(naming.KindTopic, at), &sns.TopicArgs{
			Name:      pulumi.String(topic.SNS),
			FifoTopic: pulumi.Bool(topic.Spec.Ordered),
			Tags:      tags,
		}, pulumi.DeleteBeforeReplace(true))
		if err != nil {
			return err
		}
	}
	queueNames := pulumi.StringMap{}
	for _, queue := range topic.Queues {
		consumer := queue.Consumer.Name
		deadLetter, err := sqs.NewQueue(ctx, naming.ResourceID(naming.KindTopic, at, consumer, "dead-letters"), &sqs.QueueArgs{
			Name:                    pulumi.String(queue.DeadLetter),
			FifoQueue:               pulumi.Bool(queue.FIFO),
			MessageRetentionSeconds: pulumi.Int(int(queues.MessageRetention / time.Second)),
			Tags:                    tags,
		}, pulumi.DeleteBeforeReplace(true))
		if err != nil {
			return err
		}
		redrive := deadLetter.Arn.ApplyT(func(arn string) (string, error) {
			encoded, err := json.Marshal(map[string]any{"deadLetterTargetArn": arn, "maxReceiveCount": queues.MaxReceiveCount})
			return string(encoded), err
		}).(pulumi.StringOutput)
		created, err := sqs.NewQueue(ctx, naming.ResourceID(naming.KindTopic, at, consumer), &sqs.QueueArgs{
			Name:                      pulumi.String(queue.Name),
			FifoQueue:                 pulumi.Bool(queue.FIFO),
			ContentBasedDeduplication: pulumi.Bool(queue.FIFO && topic.IsTask() && topic.Spec.Cron != ""),
			VisibilityTimeoutSeconds:  pulumi.Int(visibility(queue)),
			MessageRetentionSeconds:   pulumi.Int(int(queues.MessageRetention / time.Second)),
			RedrivePolicy:             redrive,
			Tags:                      tags,
		}, pulumi.DeleteBeforeReplace(true))
		if err != nil {
			return err
		}
		queueNames[consumer] = created.Name
		if snsTopic == nil {
			continue
		}
		policy := pulumi.All(created.Arn, snsTopic.Arn).ApplyT(func(arns []any) (string, error) {
			return snsDeliveryPolicy(fmt.Sprint(arns[0]), fmt.Sprint(arns[1]))
		}).(pulumi.StringOutput)
		allowed, err := sqs.NewQueuePolicy(ctx, naming.ResourceID(naming.KindTopic, at, consumer, "delivery"), &sqs.QueuePolicyArgs{
			QueueUrl: created.Url,
			Policy:   policy,
		})
		if err != nil {
			return err
		}
		if _, err := sns.NewTopicSubscription(ctx, naming.ResourceID(naming.KindTopic, at, consumer, "subscription"), &sns.TopicSubscriptionArgs{
			Topic:              snsTopic.Arn,
			Protocol:           pulumi.String("sqs"),
			Endpoint:           created.Arn,
			RawMessageDelivery: pulumi.Bool(true),
		}, pulumi.DependsOn([]pulumi.Resource{allowed})); err != nil {
			return err
		}
	}
	output := pulumi.Map{outputKeyTopicQueues: queueNames}
	if snsTopic != nil {
		output[outputKeyTopicARN] = snsTopic.Arn
	}
	ctx.Export(at, output)
	return nil
}

func snsDeliveryPolicy(queueARN, topicARN string) (string, error) {
	encoded, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Effect":    "Allow",
			"Principal": map[string]any{"Service": snsServicePrincipal},
			"Action":    "sqs:SendMessage",
			"Resource":  queueARN,
			"Condition": map[string]any{"ArnEquals": map[string]any{"aws:SourceArn": topicARN}},
		}},
	})
	return string(encoded), err
}

func TasksPolicy(table StateTable, project, env string, topics []Topic, wakes []string) (string, error) {
	region, account := table.Region, component.AccountOfARN(table.ARN)
	var queueARNs, topicARNs []string
	for _, topic := range topics {
		if topic.SNS != "" {
			topicARNs = append(topicARNs, SNSTopicARN(region, account, topic.SNS))
		}
		for _, queue := range topic.Queues {
			queueARNs = append(queueARNs, SQSQueueARN(region, account, queue.Name))
		}
	}
	statements := []any{
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"dynamodb:DeleteItem", "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:Query", "dynamodb:UpdateItem"},
			"Resource": table.ARN,
			"Condition": map[string]any{
				"ForAllValues:StringLike": map[string]any{"dynamodb:LeadingKeys": []string{naming.TaskKeyPrefix(project, env) + "*"}},
			},
		},
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"sqs:ChangeMessageVisibility", "sqs:GetQueueUrl", "sqs:SendMessage"},
			"Resource": queueARNs,
		},
	}
	if len(topicARNs) > 0 {
		statements = append(statements, map[string]any{"Effect": "Allow", "Action": "sns:Publish", "Resource": topicARNs})
	}
	if len(wakes) > 0 {
		statements = append(statements, map[string]any{"Effect": "Allow", "Action": []string{"ecs:DescribeServices", "ecs:UpdateService"}, "Resource": wakes})
	}
	encoded, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	return string(encoded), err
}

func CollectBinding(table StateTable, project, env string, resource provider.Resource) (*bindingsv1.Binding, error) {
	if component.AccountOfARN(table.ARN) == "" {
		return nil, fmt.Errorf("%s keeps its runs in this account's state table, and this deploy resolved no ARN for it", resource.Declared)
	}
	topics := Of(project, env, []provider.Resource{resource})
	if len(topics) != 1 {
		return nil, fmt.Errorf("%s declares no consumers, so nothing runs what it is sent", resource.Declared)
	}
	binding := &bindingsv1.Binding{Name: resource.Name}
	if topics[0].IsTask() {
		binding.Properties = &bindingsv1.Binding_Task{Task: &bindingsv1.TaskProperties{}}
	} else {
		binding.Properties = &bindingsv1.Binding_Topic{Topic: &bindingsv1.TopicProperties{}}
	}
	return binding, nil
}

const (
	MaxUnbatchedSize = 10
)

func BatchingWindowSeconds(queue Queue) int {
	batch := queue.Consumer.Batch
	if batch == nil || batch.Size <= 0 || queue.FIFO {
		return 0
	}
	window := int(batch.Timeout / time.Second)
	if batch.Size > MaxUnbatchedSize {
		window = max(window, 1)
	}
	return window
}

func ReachesTasks(app *provider.AppSpec) bool {
	if len(app.Workers) > 0 {
		return true
	}
	for _, binding := range app.Grants {
		if binding.Type == provider.BindingTask || binding.Type == provider.BindingTopic {
			return true
		}
	}
	return false
}
