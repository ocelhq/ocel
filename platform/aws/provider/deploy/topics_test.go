package deploy

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	pulumi "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
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
	}
	rec := &inputRecorder{}
	release := &release{cfg: Config{Region: "us-east-1", StateTableARN: testStateTableARN}}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return release.declareTopics(pctx, "shop", "prod", resources) }, pulumi.WithMocks("shop", "prod--infra", rec)); err != nil {
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
		{"resize", "resize", false, 930},
		{"sequence", "sequence", true, 930},
		{"orders", "audit-log", false, 930},
		{"orders", "ledger-entry", false, 120},
	} {
		queue, made := queuesMade[queues.QueueName("shop", "prod", consumer.topic, consumer.consumer, consumer.fifo)]
		if !made {
			t.Errorf("no queue for %s's consumer %s among %v", consumer.topic, consumer.consumer, slices.Collect(mapKeys(queuesMade)))
			continue
		}
		if queue["fifoQueue"].BoolValue() != consumer.fifo || queue["visibilityTimeoutSeconds"].NumberValue() != consumer.visibility {
			t.Errorf("%s's queue = fifo %v visibility %v, want fifo %v and %vs, past its worker's timeout", consumer.consumer, queue["fifoQueue"], queue["visibilityTimeoutSeconds"], consumer.fifo, consumer.visibility)
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

func TestAnAppReachingTasksMaySendToEveryQueuePublishToEveryTopicAndKeepRunsUnderTheDeploymentsKeys(t *testing.T) {
	t.Parallel()

	topics := topicsOf("shop", "prod", []provider.Resource{
		taskResource("resize", provider.ConsumerSpec{Worker: "worker"}),
		topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}),
	})
	policy, err := tasksPolicy("us-east-1", mockAccount, testStateTableARN, "shop", "prod", topics)
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
		sqsQueueARN("us-east-1", mockAccount, queues.QueueName("shop", "prod", "resize", "resize", false)),
		sqsQueueARN("us-east-1", mockAccount, queues.QueueName("shop", "prod", "orders", "audit-log", false)),
		snsTopicARN("us-east-1", mockAccount, queues.TopicName("shop", "prod", "orders", false)),
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
	binding, err := collectTopicBinding(Config{Region: "us-east-1", StateTableARN: testStateTableARN}, "shop", "prod", topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}))
	if err != nil || binding.GetTopic() == nil {
		t.Fatalf("collectTopicBinding = %T, %v, want a topic binding", binding.GetProperties(), err)
	}
}

func TestTheQueueManifestNamesEachTopicsQueuesAndSNSTopic(t *testing.T) {
	t.Parallel()

	cfg := Config{Region: "us-east-1", StateTable: "ocel-state", StateTableARN: testStateTableARN}
	manifest, err := queueManifest(cfg, "shop", "prod", []provider.Resource{
		taskResource("resize", provider.ConsumerSpec{Worker: "worker"}),
		topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Table != "ocel-state" || manifest.KeyPrefix != naming.TaskKeyPrefix("shop", "prod") {
		t.Errorf("manifest table %q prefix %q, want the state table and the deployment's task keys", manifest.Table, manifest.KeyPrefix)
	}
	if got := manifest.Topics["resize"]; got.SNS != "" || got.Queues["resize"] != queues.QueueName("shop", "prod", "resize", "resize", false) {
		t.Errorf("resize = %+v, want its queue and no SNS topic", got)
	}
	if got := manifest.Topics["orders"]; got.SNS != snsTopicARN("us-east-1", mockAccount, queues.TopicName("shop", "prod", "orders", false)) {
		t.Errorf("orders = %+v, want its SNS topic's ARN", got)
	}
}

func TestAWorkerIsAFunctionWithNoURLThatEachOfItsQueuesInvokesWithPartialBatchFailures(t *testing.T) {
	t.Parallel()

	topics := topicsOf("shop", "prod", []provider.Resource{
		taskResource("limited", provider.ConsumerSpec{Worker: "worker", Concurrency: 2}),
		taskResource("laned", provider.ConsumerSpec{Worker: "worker", Concurrency: 1}),
		taskResource("tally", provider.ConsumerSpec{Worker: "worker", Batch: &provider.BatchPolicy{Size: 50}}),
		taskResource("heartbeat", provider.ConsumerSpec{Worker: "worker"}, cronEveryMinute),
		taskResource("alpha", provider.ConsumerSpec{Worker: "capped", Concurrency: 10}),
	})
	work := &workersWork{
		project:  "shop",
		stack:    workersStack("prod", "web"),
		topics:   topics,
		workers:  []provider.WorkerSpec{{Name: "worker"}, {Name: "capped", Concurrency: 2}},
		args:     functionArgs{Runtime: defaultFunctionRuntime, Handler: "index.mjs", Arch: "arm64", MemorySizeMB: 1024, Tags: map[string]string{"ocel:managed-by": "ocel"}},
		artifact: artifactRef{Bucket: "artifacts", Key: "web.zip"},
		env:      map[string]string{"APP_VAR": "1"},
		role:     executionRole{App: "web", Boundary: testBoundaryARN},
		layer:    "arn:aws:lambda:us-east-1:123456789012:layer:ocel-runtime:1",
		region:   "us-east-1",
		account:  mockAccount,
		table:    testStateTableARN,
		prefix:   naming.TaskKeyPrefix("shop", "prod"),
		group:    queues.ScheduleGroupName("shop", "prod"),
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run the workers program: %v", err)
	}

	functions := rec.registered("aws:lambda/function:Function")
	if len(functions) != 2 {
		t.Fatalf("functions = %v, want one per worker", functions)
	}
	if urls := rec.registered("aws:lambda/functionUrl:FunctionUrl"); len(urls) != 0 {
		t.Errorf("function urls = %v, want none: a worker takes no public traffic", urls)
	}
	for _, name := range functions {
		inputs := rec.inputs("aws:lambda/function:Function", name)
		env := inputs["environment"].ObjectValue()["variables"].ObjectValue()
		worker := env["OCEL_WORKER"].StringValue()
		if worker == "" || env["APP_VAR"].StringValue() != "1" {
			t.Errorf("worker function %s env = %v, want the app's env and the worker it runs", name, env)
		}
		if worker == "capped" && env[queues.WorkerConcurrencyEnv].StringValue() != "2" {
			t.Errorf("capped's env = %v, want its concurrency of 2", env)
		}
		if inputs["timeout"].NumberValue() != 900 {
			t.Errorf("%s timeout = %v, want the 900s ceiling when a consumer names no maxDuration", name, inputs["timeout"])
		}
	}
	mappings := map[string]resource.PropertyMap{}
	for _, name := range rec.registered("aws:lambda/eventSourceMapping:EventSourceMapping") {
		inputs := rec.inputs("aws:lambda/eventSourceMapping:EventSourceMapping", name)
		mappings[inputs["eventSourceArn"].StringValue()] = inputs
		if tags, ok := inputs["tags"]; !ok || tags.ObjectValue()["ocel:managed-by"].StringValue() != "ocel" {
			t.Errorf("mapping %s tags = %v, want ocel:managed-by=ocel so the credential may create, update and delete it", name, inputs["tags"])
		}
	}
	if len(mappings) != 5 {
		t.Fatalf("event source mappings = %d, want one per queue the workers serve", len(mappings))
	}
	mapping := func(task string) resource.PropertyMap {
		return mappings[sqsQueueARN("us-east-1", mockAccount, queues.QueueName("shop", "prod", task, task, false))]
	}
	for task, want := range map[string]struct {
		batch, concurrency, window float64
	}{
		"limited": {1, 2, 0},
		"laned":   {1, 2, 0},
		"tally":   {50, 0, 1},
		"alpha":   {1, 2, 0},
	} {
		got := mapping(task)
		if got["batchSize"].NumberValue() != want.batch || !slices.Contains(stringsAt(got, "functionResponseTypes"), reportBatchItemFailures) {
			t.Errorf("%s's mapping = %v, want batches of %v reporting partial failures", task, got, want.batch)
		}
		concurrency := 0.0
		if scaling, ok := got["scalingConfig"]; ok {
			concurrency = scaling.ObjectValue()["maximumConcurrency"].NumberValue()
		}
		window := 0.0
		if declared, ok := got["maximumBatchingWindowInSeconds"]; ok {
			window = declared.NumberValue()
		}
		if concurrency != want.concurrency || window != want.window {
			t.Errorf("%s's mapping = concurrency %v window %v, want %v and %v", task, concurrency, window, want.concurrency, want.window)
		}
	}
	schedules := rec.registered("aws:scheduler/schedule:Schedule")
	if len(schedules) != 1 {
		t.Fatalf("schedules = %v, want heartbeat's", schedules)
	}
	schedule := rec.inputs("aws:scheduler/schedule:Schedule", schedules[0])
	if schedule["scheduleExpression"].StringValue() != "cron(* * * * ? *)" || schedule["groupName"].StringValue() != queues.ScheduleGroupName("shop", "prod") {
		t.Errorf("schedule = %v, want every minute in the environment's group", schedule)
	}
	var input map[string]string
	if err := json.Unmarshal([]byte(schedule["target"].ObjectValue()["input"].StringValue()), &input); err != nil || input["ocelCron"] != "heartbeat" {
		t.Errorf("schedule input = %v, want the cron task it fires", schedule["target"])
	}
	policies := strings.Join(policyDocuments(rec), "\n")
	for _, queue := range []string{"limited", "laned", "tally", "heartbeat", "alpha"} {
		if !strings.Contains(policies, queues.QueueName("shop", "prod", queue, queue, false)) {
			t.Errorf("no role policy lets the workers read %s's queue", queue)
		}
	}
}

func policyDocuments(rec *inputRecorder) []string {
	var documents []string
	for _, name := range rec.registered("aws:iam/rolePolicy:RolePolicy") {
		if policy, ok := rec.inputs("aws:iam/rolePolicy:RolePolicy", name)["policy"]; ok && policy.IsString() {
			documents = append(documents, policy.StringValue())
		}
	}
	return documents
}

func TestTheSchedulerRoleCarriesTheManagedByTagTheCredentialCreatesAndPassesRolesBy(t *testing.T) {
	t.Parallel()

	work := &workersWork{
		project: "shop",
		stack:   workersStack("prod", "web"),
		topics:  topicsOf("shop", "prod", []provider.Resource{taskResource("heartbeat", provider.ConsumerSpec{Worker: "worker"}, cronEveryMinute)}),
		workers: []provider.WorkerSpec{{Name: "worker"}},
		args:    functionArgs{Runtime: defaultFunctionRuntime, Handler: "index.mjs", Arch: "arm64", MemorySizeMB: 1024, Tags: map[string]string{"ocel:managed-by": "ocel"}},
		role:    executionRole{App: "web", Boundary: testBoundaryARN, Tags: map[string]string{"ocel:managed-by": "ocel"}},
		region:  "us-east-1",
		account: mockAccount,
		table:   testStateTableARN,
		prefix:  naming.TaskKeyPrefix("shop", "prod"),
		group:   queues.ScheduleGroupName("shop", "prod"),
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run the workers program: %v", err)
	}

	roles := rec.registered("aws:iam/role:Role")
	if len(roles) != 2 {
		t.Fatalf("roles = %v, want the workers' and the scheduler's", roles)
	}
	for _, name := range roles {
		if tags, ok := rec.inputs("aws:iam/role:Role", name)["tags"]; !ok || tags.ObjectValue()["ocel:managed-by"].StringValue() != "ocel" {
			t.Errorf("role %s tags = %v, want ocel:managed-by=ocel so the credential may create it, put its policy and pass it", name, tags)
		}
	}
}
