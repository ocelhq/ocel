package deploy

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
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const (
	workerCeiling        = 15 * time.Minute
	workerSettleMargin   = 30 * time.Second
	queueRetentionSecs   = 14 * 24 * 60 * 60
	maxReceiveCount      = 1000
	snsServicePrincipal  = "sns.amazonaws.com"
	outputKeyTopicQueues = "queues"
	outputKeyTopicARN    = "topic"
)

var WorkerCeilings = []provider.WorkerCeiling{{Compute: provider.ComputeServerless, MaxDuration: workerCeiling}}

type consumerQueue struct {
	topic      string
	consumer   provider.ConsumerSpec
	name       string
	deadLetter string
	fifo       bool
}

type deployedTopic struct {
	resource provider.Resource
	declared *provider.TopicSpec
	sns      string
	queues   []consumerQueue
}

func (t deployedTopic) isTask() bool { return t.resource.Type == provider.BindingTask }

func topicsOf(project, env string, resources []provider.Resource) []deployedTopic {
	var topics []deployedTopic
	for _, resource := range resources {
		if resource.Topic == nil || resource.Binding != "" {
			continue
		}
		declared := resource.Topic
		fifo := declared.Ordered
		topic := deployedTopic{resource: resource, declared: declared}
		if resource.Type != provider.BindingTask {
			topic.sns = queues.TopicName(project, env, resource.Declared, fifo)
		}
		for _, consumer := range declared.Consumers {
			topic.queues = append(topic.queues, consumerQueue{
				topic:      resource.Declared,
				consumer:   consumer,
				name:       queues.QueueName(project, env, resource.Declared, consumer.Name, fifo),
				deadLetter: queues.DeadLetterQueueName(project, env, resource.Declared, consumer.Name, fifo),
				fifo:       fifo,
			})
		}
		topics = append(topics, topic)
	}
	slices.SortFunc(topics, func(a, b deployedTopic) int { return cmp.Compare(a.resource.Declared, b.resource.Declared) })
	return topics
}

func workerTimeout(topics []deployedTopic, worker string) time.Duration {
	longest := time.Duration(0)
	for _, topic := range topics {
		for _, queue := range topic.queues {
			if queue.consumer.Worker != worker {
				continue
			}
			maxDuration := queue.consumer.MaxDuration
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

func queueVisibilitySeconds(topics []deployedTopic, worker string) int {
	return int((workerTimeout(topics, worker) + workerSettleMargin) / time.Second)
}

func snsTopicARN(region, account, name string) string {
	return fmt.Sprintf("arn:aws:sns:%s:%s:%s", region, account, name)
}

func sqsQueueARN(region, account, name string) string {
	return fmt.Sprintf("arn:aws:sqs:%s:%s:%s", region, account, name)
}

func queueManifest(cfg Config, project, env string, resources []provider.Resource, workers map[string]queues.Worker) (queues.Manifest, error) {
	topics := topicsOf(project, env, resources)
	manifest := queues.Manifest{Table: cfg.StateTable, KeyPrefix: naming.TaskKeyPrefix(project, env), Topics: map[string]queues.Topic{}, Workers: workers}
	if len(topics) == 0 {
		return manifest, nil
	}
	account := accountOfARN(cfg.StateTableARN)
	if account == "" {
		return queues.Manifest{}, fmt.Errorf("the state table ARN %q names no account, so the topics' ARNs cannot be written", cfg.StateTableARN)
	}
	for _, topic := range topics {
		entry := queues.Topic{Declared: topic.declared, Queues: map[string]string{}}
		if topic.sns != "" {
			entry.SNS = snsTopicARN(cfg.Region, account, topic.sns)
		}
		for _, queue := range topic.queues {
			entry.Queues[queue.consumer.Name] = queue.name
		}
		manifest.Topics[topic.resource.Declared] = entry
	}
	return manifest, nil
}

func registerTopic(ctx *pulumi.Context, topic deployedTopic, visibility func(worker string) int, tags pulumi.StringMap) error {
	at := topic.resource.Name
	var snsTopic *sns.Topic
	if topic.sns != "" {
		var err error
		snsTopic, err = sns.NewTopic(ctx, naming.ResourceID(naming.KindTopic, at), &sns.TopicArgs{
			Name:      pulumi.String(topic.sns),
			FifoTopic: pulumi.Bool(topic.declared.Ordered),
			Tags:      tags,
		}, pulumi.DeleteBeforeReplace(true))
		if err != nil {
			return err
		}
	}
	queueNames := pulumi.StringMap{}
	for _, queue := range topic.queues {
		consumer := queue.consumer.Name
		deadLetter, err := sqs.NewQueue(ctx, naming.ResourceID(naming.KindTopic, at, consumer, "dead-letters"), &sqs.QueueArgs{
			Name:                    pulumi.String(queue.deadLetter),
			FifoQueue:               pulumi.Bool(queue.fifo),
			MessageRetentionSeconds: pulumi.Int(queueRetentionSecs),
			Tags:                    tags,
		}, pulumi.DeleteBeforeReplace(true))
		if err != nil {
			return err
		}
		redrive := deadLetter.Arn.ApplyT(func(arn string) (string, error) {
			encoded, err := json.Marshal(map[string]any{"deadLetterTargetArn": arn, "maxReceiveCount": maxReceiveCount})
			return string(encoded), err
		}).(pulumi.StringOutput)
		created, err := sqs.NewQueue(ctx, naming.ResourceID(naming.KindTopic, at, consumer), &sqs.QueueArgs{
			Name:                     pulumi.String(queue.name),
			FifoQueue:                pulumi.Bool(queue.fifo),
			VisibilityTimeoutSeconds: pulumi.Int(visibility(queue.consumer.Worker)),
			MessageRetentionSeconds:  pulumi.Int(queueRetentionSecs),
			RedrivePolicy:            redrive,
			Tags:                     tags,
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

func tasksPolicy(region, account, tableARN, project, env string, topics []deployedTopic) (string, error) {
	var queueARNs, topicARNs []string
	for _, topic := range topics {
		if topic.sns != "" {
			topicARNs = append(topicARNs, snsTopicARN(region, account, topic.sns))
		}
		for _, queue := range topic.queues {
			queueARNs = append(queueARNs, sqsQueueARN(region, account, queue.name))
		}
	}
	statements := []any{
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"dynamodb:DeleteItem", "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:Query", "dynamodb:UpdateItem"},
			"Resource": tableARN,
			"Condition": map[string]any{
				"ForAllValues:StringLike": map[string]any{"dynamodb:LeadingKeys": []string{naming.TaskKeyPrefix(project, env) + "*"}},
			},
		},
		map[string]any{
			"Effect":   "Allow",
			"Action":   []string{"sqs:GetQueueUrl", "sqs:SendMessage"},
			"Resource": queueARNs,
		},
	}
	if len(topicARNs) > 0 {
		statements = append(statements, map[string]any{"Effect": "Allow", "Action": "sns:Publish", "Resource": topicARNs})
	}
	encoded, err := json.Marshal(map[string]any{"Version": "2012-10-17", "Statement": statements})
	return string(encoded), err
}

func collectTopicBinding(cfg Config, project, env string, resource provider.Resource) (*bindingsv1.Binding, error) {
	if accountOfARN(cfg.StateTableARN) == "" {
		return nil, fmt.Errorf("%s keeps its runs in this account's state table, and this deploy resolved no ARN for it", resource.Declared)
	}
	topics := topicsOf(project, env, []provider.Resource{resource})
	if len(topics) != 1 {
		return nil, fmt.Errorf("%s declares no consumers, so nothing runs what it is sent", resource.Declared)
	}
	binding := &bindingsv1.Binding{Name: resource.Name}
	if topics[0].isTask() {
		binding.Properties = &bindingsv1.Binding_Task{Task: &bindingsv1.TaskProperties{}}
	} else {
		binding.Properties = &bindingsv1.Binding_Topic{Topic: &bindingsv1.TopicProperties{}}
	}
	return binding, nil
}
