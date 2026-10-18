//go:build integration

package tasks

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/pkg/runs/runsconformance"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const liveRegion = "us-east-1"

type emulator struct {
	db  *dynamodb.Client
	sqs *sqs.Client
	sns *sns.Client
}

func live(t *testing.T) emulator {
	t.Helper()
	endpoint := os.Getenv("OCEL_FLOCI_ENDPOINT")
	if endpoint == "" {
		t.Fatal("no floci emulator in the environment; run under `scripts/floci.sh run <name> -- go test ./...`")
	}
	cfg := aws.Config{
		Region:       liveRegion,
		Credentials:  credentials.NewStaticCredentialsProvider("test", "test", ""),
		BaseEndpoint: aws.String(endpoint),
	}
	return emulator{db: dynamodb.NewFromConfig(cfg), sqs: sqs.NewFromConfig(cfg), sns: sns.NewFromConfig(cfg)}
}

func uniqueName(t *testing.T) string {
	sum := fmt.Sprintf("%x", time.Now().UnixNano())
	name := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return '-'
	}, strings.ToLower(t.Name()))
	if len(name) > 40 {
		name = name[len(name)-40:]
	}
	return strings.Trim(name, "-") + "-" + sum[len(sum)-8:]
}

func (em emulator) table(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	name := uniqueName(t)
	if _, err := em.db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(name),
		BillingMode: ddbtypes.BillingModePayPerRequest,
		AttributeDefinitions: []ddbtypes.AttributeDefinition{
			{AttributeName: aws.String(keyPK), AttributeType: ddbtypes.ScalarAttributeTypeS},
			{AttributeName: aws.String(keySK), AttributeType: ddbtypes.ScalarAttributeTypeS},
		},
		KeySchema: []ddbtypes.KeySchemaElement{
			{AttributeName: aws.String(keyPK), KeyType: ddbtypes.KeyTypeHash},
			{AttributeName: aws.String(keySK), KeyType: ddbtypes.KeyTypeRange},
		},
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = em.db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(name)})
	})
	return name
}

func livePrefix(t *testing.T) string {
	return "PROJECT#live#ENV#" + uniqueName(t) + "#TASKS#"
}

func (em emulator) queue(t *testing.T, name string) (string, string) {
	t.Helper()
	ctx := context.Background()
	attributes := map[string]string{"VisibilityTimeout": "30"}
	if queues.IsFIFO(name) {
		attributes["FifoQueue"] = "true"
	}
	out, err := em.sqs.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(name), Attributes: attributes})
	if err != nil {
		t.Fatalf("create queue %s: %v", name, err)
	}
	t.Cleanup(func() { _, _ = em.sqs.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: out.QueueUrl}) })
	read, err := em.sqs.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{QueueUrl: out.QueueUrl, AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn}})
	if err != nil {
		t.Fatalf("read queue %s: %v", name, err)
	}
	return aws.ToString(out.QueueUrl), read.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]
}

func (em emulator) deploy(t *testing.T, declared map[string]*provider.TopicSpec) queues.Topology {
	t.Helper()
	ctx := context.Background()
	topology := queues.Topology{Table: em.table(t), KeyPrefix: livePrefix(t), Topics: map[string]queues.Topic{}}
	suffix := uniqueName(t)
	fifo := func(name string, ordered bool) string {
		if ordered {
			return name + ".fifo"
		}
		return name
	}
	for name, spec := range declared {
		topic := queues.Topic{Declared: spec, Queues: map[string]string{}}
		if !runs.IsTask(spec) {
			attributes := map[string]string{}
			if spec.Ordered {
				attributes["FifoTopic"] = "true"
			}
			out, err := em.sns.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(fifo("t-"+name+"-"+suffix, spec.Ordered)), Attributes: attributes})
			if err != nil {
				t.Fatalf("create topic %s: %v", name, err)
			}
			topic.SNS = aws.ToString(out.TopicArn)
			t.Cleanup(func() { _, _ = em.sns.DeleteTopic(context.Background(), &sns.DeleteTopicInput{TopicArn: out.TopicArn}) })
		}
		for _, consumer := range spec.Consumers {
			queue := fifo("q-"+name+"-"+consumer.Name+"-"+suffix, spec.Ordered)
			_, arn := em.queue(t, queue)
			topic.Queues[consumer.Name] = queue
			if topic.SNS == "" {
				continue
			}
			if _, err := em.sns.Subscribe(ctx, &sns.SubscribeInput{
				TopicArn:   aws.String(topic.SNS),
				Protocol:   aws.String("sqs"),
				Endpoint:   aws.String(arn),
				Attributes: map[string]string{"RawMessageDelivery": "true"},
			}); err != nil {
				t.Fatalf("subscribe %s to %s: %v", queue, topic.SNS, err)
			}
		}
		topology.Topics[name] = topic
	}
	return topology
}

func TestTheDynamoDBStoreKeepsRunsAndRecordsAsEveryRunStoreMust(t *testing.T) {
	runsconformance.Store(t, func(t *testing.T) runs.Store {
		em := live(t)
		return NewStore(em.db, em.table(t), livePrefix(t))
	})
}

func TestTheEngineOverSQSAndSNSKeepsEveryRunSemantic(t *testing.T) {
	runsconformance.Engine(t, func(t *testing.T, topics map[string]*provider.TopicSpec) runsconformance.Wiring {
		em := live(t)
		topology := em.deploy(t, topics)
		queue := NewQueue(topology, em.sqs, em.sns, nil)
		return runsconformance.Wiring{
			Store: NewStore(em.db, topology.Table, topology.KeyPrefix),
			Queue: queue,
			Receiver: NewReceiver(queue, em.sqs, func(_ context.Context, task string, _ time.Time) error {
				return fmt.Errorf("no schedule fires %s here", task)
			}),
		}
	})
}

func TestLiveASlotSetHandsOutNoMoreThanItsLimitUntilOneIsReleased(t *testing.T) {
	ctx := context.Background()
	em := live(t)
	slots := NewSlots(em.db, em.table(t), livePrefix(t))
	set := runs.SlotSet{Name: "consumer#emails#send", Limit: 2}
	until := time.Now().Add(time.Minute)

	first, ok, err := slots.Take(ctx, set, "a", until)
	if err != nil || !ok {
		t.Fatalf("Take a = %v, %v, want a slot", ok, err)
	}
	if _, ok, err := slots.Take(ctx, set, "b", until); err != nil || !ok {
		t.Fatalf("Take b = %v, %v, want the second slot", ok, err)
	}
	if _, ok, err := slots.Take(ctx, set, "c", until); err != nil || ok {
		t.Fatalf("Take c = %v, %v, want none while both are held", ok, err)
	}
	if err := slots.Renew(ctx, first, until.Add(time.Minute)); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if err := slots.Release(ctx, first); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, ok, err := slots.Take(ctx, set, "c", until); err != nil || !ok {
		t.Errorf("Take c after a released = %v, %v, want the freed slot", ok, err)
	}
	expired, ok, err := slots.Take(ctx, runs.SlotSet{Name: "worker#media", Limit: 1}, "d", time.Now().Add(-time.Second))
	if err != nil || !ok {
		t.Fatalf("Take d = %v, %v", ok, err)
	}
	if _, ok, err := slots.Take(ctx, expired.Set, "e", until); err != nil || !ok {
		t.Errorf("Take e over a lease that ran out = %v, %v, want the slot", ok, err)
	}
	if err := slots.Release(ctx, expired); err != nil {
		t.Errorf("Release by a holder that lost the slot = %v, want nothing done and no error", err)
	}
}
