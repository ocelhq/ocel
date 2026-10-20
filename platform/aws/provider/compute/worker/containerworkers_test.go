package worker

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func TestAContainerWorkerIsAServiceWithNoLoadBalancerThatSitsAtNoTasks(t *testing.T) {
	t.Parallel()

	_, _, hosted := hostingContainerWorkers(t, mediaOnContainers)
	rec := programRecorded(t, hosted)

	names := rec.registered("aws:ecs/service:Service")
	if len(names) != 1 {
		t.Fatalf("declared %d services, want one for the one worker: %v", len(names), names)
	}
	service := rec.inputs("aws:ecs/service:Service", names[0])
	if service["name"].StringValue() != container.WorkerServiceName("shop", "prod", "web", "media") {
		t.Errorf("service name = %q, want the one the topology tells a sender to wake", service["name"].StringValue())
	}
	if service["desiredCount"].NumberValue() != 0 {
		t.Errorf("desired count = %v, want 0: no worker is billed idle", service["desiredCount"])
	}
	if lbs, present := service["loadBalancers"]; present && len(lbs.ArrayValue()) > 0 {
		t.Errorf("load balancers = %v, want none: a worker takes no traffic", lbs)
	}
	if service["launchType"].StringValue() != "FARGATE" || service["cluster"].StringValue() != fixtureContainerInfra().Cluster {
		t.Errorf("service runs %v on %v, want Fargate in the tier's cluster", service["launchType"], service["cluster"])
	}
	if ignored := rec.ignoredOf("aws:ecs/service:Service"); !slices.Contains(ignored, "desiredCount") {
		t.Errorf("ignored changes = %v, want desiredCount: wakes and scaling set it, and a deploy must not put it back", ignored)
	}
}

func TestAContainerWorkerRunsTheAppsImageAsAWorkerNamingTheQueuesItServes(t *testing.T) {
	t.Parallel()

	_, _, hosted := hostingContainerWorkers(t, mediaOnContainers)
	rec := programRecorded(t, hosted)

	task := recordedOf(t, rec, "aws:ecs/taskDefinition:TaskDefinition")
	if !task["containerDefinitions"].IsSecret() {
		t.Fatal("containerDefinitions is not a secret, so every delivered value would sit in the state checkpoint in the clear")
	}
	var definitions []map[string]any
	if err := json.Unmarshal([]byte(task["containerDefinitions"].SecretValue().Element.StringValue()), &definitions); err != nil || len(definitions) != 1 {
		t.Fatalf("containerDefinitions = %v, want one container: %v", task["containerDefinitions"], err)
	}
	definition := definitions[0]
	if definition["image"] != containerImage {
		t.Errorf("image = %v, want the app's own image", definition["image"])
	}
	if ports, present := definition["portMappings"]; present && ports != nil {
		t.Errorf("port mappings = %v, want none: a worker listens on loopback only", ports)
	}
	if definition["stopTimeout"] != float64(120) {
		t.Errorf("stopTimeout = %v, want 120, the longest Fargate allows, so an attempt can stop and give its run back", definition["stopTimeout"])
	}
	env := map[string]string{}
	for _, pair := range definition["environment"].([]any) {
		entry := pair.(map[string]any)
		env[entry["name"].(string)] = entry["value"].(string)
	}
	if env[processenv.WorkerEnvVar] != "media" {
		t.Errorf("%s = %q, want the worker's name", processenv.WorkerEnvVar, env[processenv.WorkerEnvVar])
	}
	if env["AWS_REGION"] != "us-east-1" {
		t.Errorf("AWS_REGION = %q, want the region of the queues, which the SDK in the container reads", env["AWS_REGION"])
	}
	if _, leaked := env[edge.OriginSecretVar]; leaked {
		t.Errorf("a worker was handed %s, which only a front that guards a public port needs", edge.OriginSecretVar)
	}
	if _, fronted := env[edge.OriginFrontedVar]; fronted {
		t.Errorf("a worker was handed %s, and it is handed no secret: it would refuse to start", edge.OriginFrontedVar)
	}
	topology, err := queues.Parse([]byte(env[queues.EnvVar]))
	if err != nil {
		t.Fatalf("%s = %q: %v", queues.EnvVar, env[queues.EnvVar], err)
	}
	if topology.Topics["resize"].Queues["resize"] != queues.QueueName("shop", "prod", "resize", "resize", false) {
		t.Errorf("topology topics = %+v, want the queues the deploy made", topology.Topics)
	}
	media := topology.Workers["media"]
	if media.Concurrency != 2 || media.Service == nil || media.Service.Name != container.WorkerServiceName("shop", "prod", "web", "media") {
		t.Errorf("topology worker = %+v, want its concurrency and the service it sets to no tasks when idle", media)
	}
}

func TestAContainerWorkerScalesOutOnABacklogToTheAppsMaxInstancesAndInNever(t *testing.T) {
	t.Parallel()

	_, _, hosted := hostingContainerWorkers(t, mediaOnContainers)
	rec := programRecorded(t, hosted)

	targets := rec.registered("aws:appautoscaling/target:Target")
	if len(targets) != 1 {
		t.Fatalf("declared %d scalable targets, want one", len(targets))
	}
	target := rec.inputs("aws:appautoscaling/target:Target", targets[0])
	if target["minCapacity"].NumberValue() != 0 || target["maxCapacity"].NumberValue() != 3 {
		t.Errorf("target spans %v to %v, want 0 to the app's 3 instances", target["minCapacity"], target["maxCapacity"])
	}
	policies := rec.registered("aws:appautoscaling/policy:Policy")
	if len(policies) != 1 {
		t.Fatalf("declared %d scaling policies, want one step policy out: the worker scales itself in", len(policies))
	}
	policy := rec.inputs("aws:appautoscaling/policy:Policy", policies[0])
	if policy["policyType"].StringValue() != "StepScaling" {
		t.Errorf("policy type = %v, want StepScaling", policy["policyType"])
	}
	step := policy["stepScalingPolicyConfiguration"].ObjectValue()
	adjustment := step["stepAdjustments"].ArrayValue()[0].ObjectValue()
	if step["adjustmentType"].StringValue() != "ChangeInCapacity" || adjustment["scalingAdjustment"].NumberValue() != 1 {
		t.Errorf("step = %v, want one more task per breach", step)
	}
}

func TestAContainerWorkersBacklogAlarmWatchesTheVisibleMessagesOfEveryQueueItServesNineAtATime(t *testing.T) {
	t.Parallel()

	_, spec, hosted := hostingContainerWorkers(t, mediaOnContainers)
	spec.App.Topics = nil
	for i := range 11 {
		spec.App.Topics = append(spec.App.Topics, taskResource("job-"+strconv.Itoa(i), provider.ConsumerSpec{Worker: "media"}))
	}
	hosted.topics = topic.Of("shop", "prod", spec.App.Topics)
	rec := programRecorded(t, hosted)

	alarms := rec.registered("aws:cloudwatch/metricAlarm:MetricAlarm")
	if len(alarms) != 2 {
		t.Fatalf("declared %d alarms for 11 queues, want 2: a metric math alarm takes ten metrics", len(alarms))
	}
	watched := 0
	for _, name := range alarms {
		alarm := rec.inputs("aws:cloudwatch/metricAlarm:MetricAlarm", name)
		if alarm["comparisonOperator"].StringValue() != "GreaterThanThreshold" || alarm["threshold"].NumberValue() != 0 || alarm["treatMissingData"].StringValue() != "notBreaching" {
			t.Errorf("alarm %s = %v, want it to breach on any visible message and to read a silent queue as empty", name, alarm)
		}
		if !strings.HasPrefix(alarm["name"].StringValue(), "ocel-app-") {
			t.Errorf("alarm named %q, want a name under the app scope the deploy credential may touch", alarm["name"].StringValue())
		}
		queries := alarm["metricQueries"].ArrayValue()
		if len(queries) > 10 {
			t.Errorf("alarm %s has %d metric queries, more than CloudWatch accepts", name, len(queries))
		}
		for _, query := range queries {
			stat, isMetric := query.ObjectValue()["metric"]
			if !isMetric {
				continue
			}
			watched++
			metric := stat.ObjectValue()
			if metric["metricName"].StringValue() != "ApproximateNumberOfMessagesVisible" || metric["namespace"].StringValue() != "AWS/SQS" {
				t.Errorf("metric = %v, want the visible messages of an SQS queue", metric)
			}
		}
		if actions := alarm["alarmActions"].ArrayValue(); len(actions) != 1 {
			t.Errorf("alarm actions = %v, want the one step policy", actions)
		}
	}
	if watched != 11 {
		t.Errorf("alarms watch %d queues, want all 11 the worker serves", watched)
	}
}

func TestAContainerWorkersTaskRoleReachesOnlyTheQueuesItServesAndItsOwnService(t *testing.T) {
	t.Parallel()

	_, _, hosted := hostingContainerWorkers(t, mediaOnContainers)
	rec := programRecorded(t, hosted)

	role := recordedOf(t, rec, "aws:iam/role:Role")
	if role["permissionsBoundary"].StringValue() != componenttest.BoundaryARN || !strings.Contains(role["assumeRolePolicy"].StringValue(), iam.ECSTasksPrincipal) {
		t.Errorf("role = %v, want an ECS task role under the app boundary", role)
	}
	var policies []string
	for _, name := range rec.registered("aws:iam/rolePolicy:RolePolicy") {
		policies = append(policies, rec.inputs("aws:iam/rolePolicy:RolePolicy", name)["policy"].StringValue())
	}
	all := strings.Join(policies, "\n")
	for _, want := range []string{
		topic.SQSQueueARN("us-east-1", mockAccount, queues.QueueName("shop", "prod", "resize", "resize", false)),
		"sqs:ReceiveMessage",
		"sqs:ChangeMessageVisibility",
		"sqs:GetQueueAttributes",
		"service/ocel-prod-containers/" + container.WorkerServiceName("shop", "prod", "web", "media"),
	} {
		if !strings.Contains(all, want) {
			t.Errorf("the worker's policies %s name no %s", all, want)
		}
	}
	for _, policy := range policies {
		if strings.Contains(policy, "sqs:ReceiveMessage") && strings.Contains(policy, queues.QueueName("shop", "prod", "orders", "audit-log", false)) {
			t.Errorf("the worker may receive from a queue another worker serves: %s", policy)
		}
	}
}

func TestAContainerWorkersStackExportsEachWorkersServiceForTheDeployToRecord(t *testing.T) {
	t.Parallel()

	_, _, hosted := hostingContainerWorkers(t, mediaOnContainers)
	outputs := auto.OutputMap{resources.WorkerName("media"): auto.OutputValue{Value: map[string]any{container.OutputKeyPhysical: "ocel-app-shop-prod-web-media"}}}

	result, err := hosted.Decode(outputs)
	if err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if len(result.Containers) != 1 || result.Containers[0].Name != resources.WorkerName("media") || result.Containers[0].Physical != "ocel-app-shop-prod-web-media" || result.Containers[0].URL != "" || result.Containers[0].Image != containerImage {
		t.Errorf("Containers = %+v, want the media worker by its service, with no URL: it takes no traffic", result.Containers)
	}
	if _, err := hosted.Decode(auto.OutputMap{}); err == nil {
		t.Error("Decode() of a stack that exported no service succeeded, so a deploy would record a worker it cannot find")
	}
}

var webAsItsOwnWorker = provider.WorkerSpec{Name: "web", Concurrency: 2, App: "web", Compute: provider.ComputeContainer}

func TestAContainerAppThatOnlyHostsItsWorkerDeclaresNoWebServiceOfItsOwn(t *testing.T) {
	t.Parallel()

	at, spec, _ := hostingContainerWorkers(t, webAsItsOwnWorker)
	work, err := definitionOf(at, spec)
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.Declare(pctx) }, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run the app's program: %v", err)
	}

	for _, kind := range []string{"aws:ecs/service:Service", "aws:ecs/taskDefinition:TaskDefinition", "aws:lb/targetGroup:TargetGroup", "aws:lb/listenerRule:ListenerRule"} {
		if declared := rec.registered(kind); len(declared) > 0 {
			t.Errorf("declared %s %v, want none: a worker app takes no traffic, so a web service would only bill its floor idle", kind, declared)
		}
	}
}

func TestAContainerWorkerAppsMinInstancesIsItsWorkersFloorAndAWebAppsIsNot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		worker provider.WorkerSpec
		floor  float64
	}{
		{worker: webAsItsOwnWorker, floor: 1},
		{worker: mediaOnContainers, floor: 0},
	} {
		_, _, hosted := hostingContainerWorkers(t, tc.worker)
		rec := programRecorded(t, hosted)

		service := recordedOf(t, rec, "aws:ecs/service:Service")
		if service["desiredCount"].NumberValue() != tc.floor {
			t.Errorf("worker %s's service starts at %v tasks, want %v", tc.worker.Name, service["desiredCount"], tc.floor)
		}
		target := recordedOf(t, rec, "aws:appautoscaling/target:Target")
		if target["minCapacity"].NumberValue() != tc.floor {
			t.Errorf("worker %s scales in to %v tasks, want %v", tc.worker.Name, target["minCapacity"], tc.floor)
		}
		topology, err := queues.Parse([]byte(hosted.topology))
		if err != nil {
			t.Fatal(err)
		}
		if served := topology.Workers[tc.worker.Name].Service; served == nil || float64(served.Floor) != tc.floor {
			t.Errorf("worker %s's topology names service %+v, want a floor of %v for the worker to scale itself in to", tc.worker.Name, served, tc.floor)
		}
	}
}

func TestACronTaskOnAContainerWorkerIsScheduledOntoItsQueueByARoleThatMaySendThereAlone(t *testing.T) {
	t.Parallel()

	nightly := taskResource("nightly", provider.ConsumerSpec{Worker: "media"}, cronEveryMinute)
	_, _, hosted := hostingContainerWorkersBeside(t, []provider.Resource{nightly}, mediaOnContainers)
	rec := programRecorded(t, hosted)

	queue := "arn:aws:sqs:us-east-1:" + mockAccount + ":" + topic.Of("shop", "prod", []provider.Resource{nightly})[0].Queues[0].Name
	schedules := rec.registered("aws:scheduler/schedule:Schedule")
	if len(schedules) != 1 {
		t.Fatalf("declared %d schedules, want one for the nightly task", len(schedules))
	}
	schedule := rec.inputs("aws:scheduler/schedule:Schedule", schedules[0])
	target := schedule["target"].ObjectValue()
	if target["arn"].StringValue() != queue {
		t.Errorf("the schedule targets %v, want the task's queue %s: the worker reads its queue, and nothing else starts it", target["arn"], queue)
	}
	if input := target["input"].StringValue(); !strings.Contains(input, `"ocelCron":"nightly"`) || !strings.Contains(input, "<aws.scheduler.scheduled-time>") {
		t.Errorf("the schedule sends %q, want the cron marker naming the task and the time it fired for", input)
	}
	if schedule["groupName"].StringValue() != queues.ScheduleGroupName("shop", "prod") {
		t.Errorf("the schedule is in group %v, want the deployment's", schedule["groupName"])
	}

	var granted bool
	for _, name := range rec.registered("aws:iam/rolePolicy:RolePolicy") {
		policy := rec.inputs("aws:iam/rolePolicy:RolePolicy", name)["policy"].StringValue()
		if !strings.Contains(policy, "sqs:SendMessage") || strings.Contains(policy, "sqs:ReceiveMessage") {
			continue
		}
		if strings.Contains(policy, "dynamodb") {
			continue
		}
		granted = true
		var document struct {
			Statement []struct {
				Action   any `json:"Action"`
				Resource any `json:"Resource"`
			} `json:"Statement"`
		}
		if err := json.Unmarshal([]byte(policy), &document); err != nil {
			t.Fatal(err)
		}
		if got, _ := json.Marshal(document.Statement[0].Resource); len(document.Statement) != 1 || string(got) != `["`+queue+`"]` {
			t.Errorf("the scheduler may send to %s, want the nightly queue alone", got)
		}
	}
	if !granted {
		t.Error("no role policy lets the scheduler send to the task's queue")
	}
}

func TestAnOrderedCronTaskOnAContainerWorkerIsScheduledIntoOneMessageGroupOfAQueueThatDeduplicatesByContent(t *testing.T) {
	t.Parallel()

	nightly := taskResource("nightly", provider.ConsumerSpec{Worker: "media"}, cronEveryMinute, orderedTopic)
	_, _, hosted := hostingContainerWorkersBeside(t, []provider.Resource{nightly}, mediaOnContainers)
	rec := programRecorded(t, hosted)
	schedule := recordedOf(t, rec, "aws:scheduler/schedule:Schedule")
	if group := schedule["target"].ObjectValue()["sqsParameters"].ObjectValue()["messageGroupId"].StringValue(); group == "" {
		t.Errorf("the schedule sends to a FIFO queue with no message group, which SQS refuses")
	}

	declared := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error {
		return declareTopicsAndTasks(pctx, "us-east-1", testStateTableARN, "shop", "prod", []provider.Resource{nightly})
	}, pulumi.WithMocks("shop", "prod--infra", declared)); err != nil {
		t.Fatalf("declare the topics: %v", err)
	}
	for _, name := range declared.registered("aws:sqs/queue:Queue") {
		queue := declared.inputs("aws:sqs/queue:Queue", name)
		if queue["name"].StringValue() == queues.QueueName("shop", "prod", "nightly", "nightly", true) && !queue["contentBasedDeduplication"].BoolValue() {
			t.Errorf("the nightly queue does not deduplicate by content, so a schedule's send, which names no deduplication id, is refused")
		}
	}
}
