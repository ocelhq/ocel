package topic

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAnAppReachingTasksMayReleaseTheMessageAnOrderedRunHoldsOnItsQueue(t *testing.T) {
	t.Parallel()

	topics := mustOf(t, []provider.Resource{taskResource("resize", provider.ConsumerSpec{Worker: "worker"})})
	policy, err := TasksPolicy(StateTable{Region: "us-east-1", ARN: testStateTableARN}, "shop", "prod", topics, nil)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Statement []struct {
			Action   []string `json:"Action"`
			Resource any      `json:"Resource"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(policy), &document); err != nil {
		t.Fatalf("the policy %s is not the shape this test reads: %v", policy, err)
	}
	queue := SQSQueueARN("us-east-1", mockAccount, queueName(t, "resize", "resize", false))
	for _, statement := range document.Statement {
		resources, _ := json.Marshal(statement.Resource)
		if slices.Contains(statement.Action, "sqs:ChangeMessageVisibility") && strings.Contains(string(resources), queue) {
			return
		}
	}
	t.Errorf("the tasks policy %s grants no sqs:ChangeMessageVisibility on %s, so canceling or rescheduling a delayed ordered run cannot release the message holding its key", policy, queue)
}

func TestAnAppReachingTasksMaySendToEveryQueuePublishToEveryTopicAndKeepRunsUnderTheDeploymentsKeys(t *testing.T) {
	t.Parallel()

	topics := mustOf(t, []provider.Resource{
		taskResource("resize", provider.ConsumerSpec{Worker: "worker"}),
		topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}),
	})
	policy, err := TasksPolicy(StateTable{Region: "us-east-1", ARN: testStateTableARN}, "shop", "prod", topics, nil)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Statement []struct {
			Action    any            `json:"Action"`
			Resource  any            `json:"Resource"`
			Condition map[string]any `json:"Condition"`
		} `json:"Statement"`
	}
	if err := json.Unmarshal([]byte(policy), &document); err != nil {
		t.Fatalf("the policy %s is not JSON: %v", policy, err)
	}
	for _, want := range []string{
		SQSQueueARN("us-east-1", mockAccount, queueName(t, "resize", "resize", false)),
		SQSQueueARN("us-east-1", mockAccount, queueName(t, "orders", "audit-log", false)),
		SNSTopicARN("us-east-1", mockAccount, topicName(t, "orders", false)),
		testStateTableARN,
		naming.TaskKeyPrefix("shop", "prod") + "*",
	} {
		if !strings.Contains(policy, want) {
			t.Errorf("the tasks policy %s names no %s", policy, want)
		}
	}
	if strings.Contains(policy, `"*"`) {
		t.Errorf("the tasks policy %s grants a wildcard resource", policy)
	}
	binding, err := CollectBinding(StateTable{Region: "us-east-1", ARN: testStateTableARN}, shopNames, topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}))
	if err != nil || binding.GetTopic() == nil {
		t.Fatalf("collectTopicBinding = %T, %v, want a topic binding", binding.GetProperties(), err)
	}
}

func TestTheQueueTopologyNamesEachTopicsQueuesAndSNSTopic(t *testing.T) {
	t.Parallel()

	topology, err := QueueTopology(StateTable{Region: "us-east-1", Name: "ocel-state", ARN: testStateTableARN}, "shop", "prod", mustOf(t, []provider.Resource{
		taskResource("resize", provider.ConsumerSpec{Worker: "worker"}),
		topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}),
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Table != "ocel-state" || topology.KeyPrefix != naming.TaskKeyPrefix("shop", "prod") {
		t.Errorf("topology table %q prefix %q, want the state table and the deployment's task keys", topology.Table, topology.KeyPrefix)
	}
	if got := topology.Topics["resize"]; got.SNS != "" || got.Queues["resize"] != queueName(t, "resize", "resize", false) {
		t.Errorf("resize = %+v, want its queue and no SNS topic", got)
	}
	if got := topology.Topics["orders"]; got.SNS != SNSTopicARN("us-east-1", mockAccount, topicName(t, "orders", false)) {
		t.Errorf("orders = %+v, want its SNS topic's ARN", got)
	}
}

type queueRecorder struct {
	mu     sync.Mutex
	queues []string
}

func (r *queueRecorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if args.TypeToken == "aws:sqs/queue:Queue" {
		r.queues = append(r.queues, args.Name)
	}
	return args.Name + "-id", args.Inputs, nil
}

func (r *queueRecorder) Call(pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return resource.PropertyMap{}, nil
}

func TestAConsumerNamedLikeAnotherConsumersDeadLetterQueueGetsAQueueOfItsOwn(t *testing.T) {
	t.Parallel()

	topics := mustOf(t, []provider.Resource{topicResource("orders",
		provider.ConsumerSpec{Name: "audit", Worker: "worker"},
		provider.ConsumerSpec{Name: "audit-dead-letters", Worker: "worker"},
	)})
	rec := &queueRecorder{}
	err := pulumi.RunErr(func(ctx *pulumi.Context) error {
		return Register(ctx, topics[0], func(Queue) int { return 30 }, nil)
	}, pulumi.WithMocks("shop", "prod--infra", rec))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	unique := slices.Compact(slices.Sorted(slices.Values(rec.queues)))
	if len(rec.queues) != 4 || len(unique) != 4 {
		t.Errorf("registered queues %v, want four apart: each consumer's queue and dead-letter queue", rec.queues)
	}
}
