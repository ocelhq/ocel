package topic

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

func TestAnAppReachingTasksMayReleaseTheMessageAnOrderedRunHoldsOnItsQueue(t *testing.T) {
	t.Parallel()

	topics := Of("shop", "prod", []provider.Resource{taskResource("resize", provider.ConsumerSpec{Worker: "worker"})})
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
	queue := SQSQueueARN("us-east-1", mockAccount, queues.QueueName("shop", "prod", "resize", "resize", false))
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

	topics := Of("shop", "prod", []provider.Resource{
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
		SQSQueueARN("us-east-1", mockAccount, queues.QueueName("shop", "prod", "resize", "resize", false)),
		SQSQueueARN("us-east-1", mockAccount, queues.QueueName("shop", "prod", "orders", "audit-log", false)),
		SNSTopicARN("us-east-1", mockAccount, queues.TopicName("shop", "prod", "orders", false)),
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
	binding, err := CollectBinding(StateTable{Region: "us-east-1", ARN: testStateTableARN}, "shop", "prod", topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}))
	if err != nil || binding.GetTopic() == nil {
		t.Fatalf("collectTopicBinding = %T, %v, want a topic binding", binding.GetProperties(), err)
	}
}

func TestTheQueueTopologyNamesEachTopicsQueuesAndSNSTopic(t *testing.T) {
	t.Parallel()

	topology, err := QueueTopology(StateTable{Region: "us-east-1", Name: "ocel-state", ARN: testStateTableARN}, "shop", "prod", Of("shop", "prod", []provider.Resource{
		taskResource("resize", provider.ConsumerSpec{Worker: "worker"}),
		topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}),
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if topology.Table != "ocel-state" || topology.KeyPrefix != naming.TaskKeyPrefix("shop", "prod") {
		t.Errorf("topology table %q prefix %q, want the state table and the deployment's task keys", topology.Table, topology.KeyPrefix)
	}
	if got := topology.Topics["resize"]; got.SNS != "" || got.Queues["resize"] != queues.QueueName("shop", "prod", "resize", "resize", false) {
		t.Errorf("resize = %+v, want its queue and no SNS topic", got)
	}
	if got := topology.Topics["orders"]; got.SNS != SNSTopicARN("us-east-1", mockAccount, queues.TopicName("shop", "prod", "orders", false)) {
		t.Errorf("orders = %+v, want its SNS topic's ARN", got)
	}
}

func TestAWorkerOnContainerComputeRunsAtMostTwelveHoursTheLongestSQSHoldsAMessage(t *testing.T) {
	t.Parallel()

	ceiling := workerCeilingOn(t, provider.ComputeContainer)
	if ceiling.Unbounded || ceiling.MaxDuration != 12*time.Hour {
		t.Errorf("container worker ceiling = %+v, want 12h: SQS extends a message's visibility to 12 hours and no further", ceiling)
	}
	if serverless := workerCeilingOn(t, provider.ComputeServerless); serverless.MaxDuration != 15*time.Minute {
		t.Errorf("serverless worker ceiling = %v, want 15m, Lambda's own", serverless.MaxDuration)
	}
}

func workerCeilingOn(t *testing.T, compute provider.Compute) provider.WorkerCeiling {
	t.Helper()
	for _, ceiling := range WorkerCeilings {
		if ceiling.Compute == compute {
			return ceiling
		}
	}
	t.Fatalf("WorkerCeilings names no ceiling for %s compute", compute)
	return provider.WorkerCeiling{}
}
