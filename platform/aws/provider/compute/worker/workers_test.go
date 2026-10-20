package worker

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/function"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func TestAWorkerIsAFunctionWithNoURLThatEachOfItsQueuesInvokesWithPartialBatchFailures(t *testing.T) {
	t.Parallel()

	topics := mustTopics(t, []provider.Resource{
		taskResource("limited", provider.ConsumerSpec{Worker: "worker", Concurrency: 2}),
		taskResource("laned", provider.ConsumerSpec{Worker: "worker", Concurrency: 1}),
		taskResource("tally", provider.ConsumerSpec{Worker: "worker", Batch: &provider.BatchPolicy{Size: 50}}),
		taskResource("heartbeat", provider.ConsumerSpec{Worker: "worker"}, cronEveryMinute),
		taskResource("alpha", provider.ConsumerSpec{Worker: "capped", Concurrency: 10}),
	})
	work := &Stack{
		names:    shopNames,
		project:  "shop",
		stack:    StackName("prod", "web"),
		topics:   topics,
		workers:  []provider.WorkerSpec{{Name: "worker"}, {Name: "capped", Concurrency: 2}},
		args:     function.Args{Runtime: component.LambdaNodeRuntime, Handler: "index.mjs", Arch: "arm64", MemorySizeMB: 1024, Tags: map[string]string{"ocel:managed-by": "ocel"}},
		artifact: function.Artifact{Bucket: "artifacts", Key: "web.zip"},
		env:      map[string]string{"APP_VAR": "1"},
		role:     function.ExecutionRole{Namespace: "ocel", Boundary: componenttest.BoundaryARN},
		layer:    "arn:aws:lambda:us-east-1:123456789012:layer:ocel-runtime:1",
		region:   "us-east-1",
		account:  mockAccount,
		table:    testStateTableARN,
		prefix:   naming.TaskKeyPrefix("shop", "prod"),
		group:    scheduleGroup(t),
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.Declare(pctx) }, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
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
		if fn := inputs["name"].StringValue(); !strings.HasPrefix(fn, "ocel-app-") {
			t.Errorf("worker function %s is named %q, want it under ocel-app-, the only functions the deploy credential may map a queue to", name, fn)
		}
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
		return mappings[topic.SQSQueueARN("us-east-1", mockAccount, queueName(t, task, task, false))]
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
	if schedule["scheduleExpression"].StringValue() != "cron(* * * * ? *)" || schedule["groupName"].StringValue() != scheduleGroup(t) {
		t.Errorf("schedule = %v, want every minute in the environment's group", schedule)
	}
	var input map[string]string
	if err := json.Unmarshal([]byte(schedule["target"].ObjectValue()["input"].StringValue()), &input); err != nil || input["ocelCron"] != "heartbeat" {
		t.Errorf("schedule input = %v, want the cron task it fires", schedule["target"])
	}
	policies := strings.Join(policyDocuments(rec), "\n")
	for _, queue := range []string{"limited", "laned", "tally", "heartbeat", "alpha"} {
		if !strings.Contains(policies, queueName(t, queue, queue, false)) {
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

	work := &Stack{
		names:   shopNames,
		project: "shop",
		stack:   StackName("prod", "web"),
		topics:  mustTopics(t, []provider.Resource{taskResource("heartbeat", provider.ConsumerSpec{Worker: "worker"}, cronEveryMinute)}),
		workers: []provider.WorkerSpec{{Name: "worker"}},
		args:    function.Args{Runtime: component.LambdaNodeRuntime, Handler: "index.mjs", Arch: "arm64", MemorySizeMB: 1024, Tags: map[string]string{"ocel:managed-by": "ocel"}},
		role:    function.ExecutionRole{Namespace: "ocel", Boundary: componenttest.BoundaryARN, Tags: map[string]string{"ocel:managed-by": "ocel"}},
		region:  "us-east-1",
		account: mockAccount,
		table:   testStateTableARN,
		prefix:  naming.TaskKeyPrefix("shop", "prod"),
		group:   scheduleGroup(t),
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.Declare(pctx) }, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
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

func TestTheWorkersProgramExportsEachWorkersLambdaNameForTheStackToDecode(t *testing.T) {
	t.Parallel()

	work := &Stack{
		names:   shopNames,
		project: "shop",
		stack:   StackName("prod", "web"),
		workers: []provider.WorkerSpec{{Name: "media"}, {Name: "ledger"}},
		args:    function.Args{Runtime: component.LambdaNodeRuntime, Handler: "index.mjs", Arch: "arm64", MemorySizeMB: 1024},
		role:    function.ExecutionRole{Namespace: "ocel", Boundary: componenttest.BoundaryARN},
		region:  "us-east-1",
		account: mockAccount,
		table:   testStateTableARN,
		prefix:  naming.TaskKeyPrefix("shop", "prod"),
	}
	rec := &inputRecorder{}
	exported := make(chan map[string]any, 1)
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error {
		if err := work.Declare(pctx); err != nil {
			return err
		}
		pulumi.Map(pctx.GetCurrentExportMap()).ToMapOutput().ApplyT(func(resolved map[string]any) error {
			exported <- resolved
			return nil
		})
		return nil
	}, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run the workers program: %v", err)
	}

	lambdas := map[string]string{}
	for _, name := range rec.registered("aws:lambda/function:Function") {
		inputs := rec.inputs("aws:lambda/function:Function", name)
		lambdas[inputs["environment"].ObjectValue()["variables"].ObjectValue()["OCEL_WORKER"].StringValue()] = inputs["name"].StringValue()
	}
	outputs := auto.OutputMap{}
	for logical, value := range <-exported {
		outputs[logical] = auto.OutputValue{Value: value}
	}
	for _, worker := range []string{"media", "ledger"} {
		fields, _ := outputs[resources.WorkerName(worker)].Value.(map[string]any)
		if lambdas[worker] == "" || fields[function.OutputKeyName] != lambdas[worker] {
			t.Errorf("export %s = %v, want the name of %s's Lambda, %q", resources.WorkerName(worker), fields, worker, lambdas[worker])
		}
	}
	decoded, err := work.Decode(outputs)
	if err != nil {
		t.Fatalf("decode the program's own exports: %v", err)
	}
	physical := map[string]string{}
	for _, fn := range decoded.Functions {
		physical[fn.Name] = fn.Physical
	}
	if len(physical) != 2 || physical[resources.WorkerName("media")] != lambdas["media"] || physical[resources.WorkerName("ledger")] != lambdas["ledger"] {
		t.Errorf("decoded %v, want each worker by its Lambda's name %v", physical, lambdas)
	}
}
