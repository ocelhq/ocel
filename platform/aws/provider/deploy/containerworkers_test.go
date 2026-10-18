package deploy

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
)

func TestOnlyAWorkerOnContainerComputeNamesTheServiceASenderWakes(t *testing.T) {
	t.Parallel()

	got := containerWorkers(environment.TierProduction, "shop", "prod", []provider.WorkerSpec{
		{Name: "media", Concurrency: 4, App: "jobs", Compute: provider.ComputeContainer},
		{Name: "worker", App: "web", Compute: provider.ComputeServerless},
	})
	if len(got) != 1 {
		t.Fatalf("containerWorkers() = %+v, want only the media worker: a function is woken by its queue, not by a sender", got)
	}
	media := got["media"]
	if media.Concurrency != 4 {
		t.Errorf("media concurrency = %d, want 4: the engine holds a worker's cap in slots", media.Concurrency)
	}
	want := &queues.Service{Cluster: awsports.ContainerClusterName(environment.TierProduction), Name: containerWorkerService("shop", "prod", "jobs", "media")}
	if media.Service == nil || *media.Service != *want {
		t.Errorf("media service = %+v, want %+v", media.Service, want)
	}
}

func TestAContainerWorkersServiceNameIsOneForEachWorkerOfEachAppAndEnvironment(t *testing.T) {
	t.Parallel()

	names := map[string]bool{}
	for _, name := range []string{
		containerWorkerService("shop", "prod", "jobs", "media"),
		containerWorkerService("shop", "prod", "jobs", "reports"),
		containerWorkerService("shop", "prod", "web", "media"),
		containerWorkerService("shop", "staging", "jobs", "media"),
		containerWorkerService("other", "prod", "jobs", "media"),
	} {
		if len(name) > 255 || names[name] {
			t.Errorf("service name %q repeats an earlier one or is longer than ECS allows", name)
		}
		names[name] = true
	}
}

func hostingContainerWorkers(t *testing.T, workers ...provider.WorkerSpec) (*release, provider.StackSpec, *containerWorkersWork) {
	t.Helper()
	return hostingContainerWorkersBeside(t, nil, workers...)
}

func hostingContainerWorkersBeside(t *testing.T, more []provider.Resource, workers ...provider.WorkerSpec) (*release, provider.StackSpec, *containerWorkersWork) {
	t.Helper()
	cfg, spec := containerStackSpec(t)
	cfg.StateTable, cfg.StateTableARN = "ocel-state", testStateTableARN
	spec.Ref.Name = naming.AppStack("prod", "web", fixedRelease(t))
	spec.App.Instances = provider.Instances{Min: 1, Max: 3}
	spec.App.Workers = workers
	spec.App.DeploymentWorkers = workers
	spec.App.Topics = []provider.Resource{
		taskResource("resize", provider.ConsumerSpec{Worker: "media", MaxDuration: time.Hour}),
		taskResource("thumbnail", provider.ConsumerSpec{Worker: "media"}),
		topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}),
	}
	spec.App.Topics = append(spec.App.Topics, more...)
	opened := releasing(t, cfg)
	work, err := opened.containerWork(spec, fixtureContainerInfra())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	hosted, err := opened.containerWorkersWork(spec, work)
	if err != nil {
		t.Fatalf("containerWorkersWork() = %v", err)
	}
	return opened, spec, hosted
}

func programRecorded(t *testing.T, hosted *containerWorkersWork) *inputRecorder {
	t.Helper()
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return hosted.run(pctx) }, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run the container workers program: %v", err)
	}
	return rec
}

var mediaOnContainers = provider.WorkerSpec{Name: "media", Concurrency: 2, App: "web", Compute: provider.ComputeContainer}

func TestAContainerWorkerIsAServiceWithNoLoadBalancerThatSitsAtNoTasks(t *testing.T) {
	t.Parallel()

	_, _, hosted := hostingContainerWorkers(t, mediaOnContainers)
	rec := programRecorded(t, hosted)

	names := rec.registered("aws:ecs/service:Service")
	if len(names) != 1 {
		t.Fatalf("declared %d services, want one for the one worker: %v", len(names), names)
	}
	service := rec.inputs("aws:ecs/service:Service", names[0])
	if service["name"].StringValue() != containerWorkerService("shop", "prod", "web", "media") {
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
	if media.Concurrency != 2 || media.Service == nil || media.Service.Name != containerWorkerService("shop", "prod", "web", "media") {
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
		"service/ocel-containers-production/" + containerWorkerService("shop", "prod", "web", "media"),
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
	outputs := auto.OutputMap{resources.WorkerName("media"): auto.OutputValue{Value: map[string]any{outputKeyContainerPhysical: "ocel-app-shop-prod-web-media"}}}

	result, err := hosted.decode(outputs)
	if err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if len(result.Containers) != 1 || result.Containers[0].Name != resources.WorkerName("media") || result.Containers[0].Physical != "ocel-app-shop-prod-web-media" || result.Containers[0].URL != "" || result.Containers[0].Image != containerImage {
		t.Errorf("Containers = %+v, want the media worker by its service, with no URL: it takes no traffic", result.Containers)
	}
	if _, err := hosted.decode(auto.OutputMap{}); err == nil {
		t.Error("Decode() of a stack that exported no service succeeded, so a deploy would record a worker it cannot find")
	}
}

func containerStacksHostingWorkers(t *testing.T, store records.Store) (*Stacks, *mockedEngine, provider.StackSpec) {
	t.Helper()
	cfg, spec := containerStackSpec(t)
	cfg.KeyValues = store
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	cfg.StateTable, cfg.StateTableARN = "ocel-state", testStateTableARN
	cfg.Parameters, cfg.KVTokenRoot = newFakeParameters(), conformanceKVTokenRoot
	cfg.SigningKeys, cfg.SigningKeyRoot = newFakeSigningKeys(), conformanceSigningKeyRoot
	spec.Ref.Name = naming.AppStack("prod", "web", fixedRelease(t))
	spec.App.Workers = []provider.WorkerSpec{mediaOnContainers}
	spec.App.DeploymentWorkers = spec.App.Workers
	spec.App.Topics = []provider.Resource{taskResource("resize", provider.ConsumerSpec{Worker: "media"})}
	outputs := containerInfraOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: containerPhysical,
	}}
	outputs[resources.WorkerName("media")] = auto.OutputValue{Value: map[string]any{outputKeyContainerPhysical: containerWorkerService("shop", "prod", "web", "media")}}
	engine := &mockedEngine{outputs: outputs}
	return stacksWith(cfg, engine), engine, spec
}

func TestAContainerAppHostingWorkersProvisionsTheirStackBesideItsOwnAndReturnsBoth(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)

	result, err := stacks.Provision(ctx, spec, progress.Discard())
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	var returned []string
	for _, container := range result.Containers {
		returned = append(returned, container.Name)
	}
	if !slices.Contains(returned, "web") || !slices.Contains(returned, resources.WorkerName("media")) || len(returned) != 2 {
		t.Errorf("Containers = %v, want the app and its media worker", returned)
	}
	if !slices.Contains(engine.stacks(), naming.ContainerWorkersStack("prod", "web").String()) {
		t.Errorf("stacks run = %v, want the workers stack %s among them", engine.stacks(), naming.ContainerWorkersStack("prod", "web"))
	}
	consumers, err := listConsumers(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	var held []string
	for _, consumer := range consumers {
		held = append(held, consumer.Key.String())
	}
	if !slices.ContainsFunc(held, func(key string) bool {
		return strings.Contains(key, naming.ContainerWorkersStack("prod", "web").String())
	}) {
		t.Errorf("container infrastructure consumers = %v, want the workers stack among them: the cluster must outlive the services in it", held)
	}
}

func TestTakingDownAnEnvironmentTakesItsContainerWorkersAndLetsGoTheirHoldOnTheClusterInfrastructure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: spec.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(app) = %v", err)
	}
	infra := provider.StackRef{Project: spec.Ref.Project, Tier: spec.Ref.Tier, Name: naming.InfraStack("prod")}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: infra}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(environment) = %v", err)
	}

	if !slices.Contains(engine.torn(), naming.ContainerWorkersStack("prod", "web").String()) {
		t.Errorf("torn down = %v, want the workers stack %s", engine.torn(), naming.ContainerWorkersStack("prod", "web"))
	}
	consumers, err := listConsumers(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(consumers) != 0 {
		t.Errorf("consumers left = %v, want none: the shared cluster can go once the last service in it has", consumers)
	}
}

func liveContainerApp(workers ...string) provider.LiveApps {
	return provider.LiveApps{Slug: "shop", Tier: environment.TierProduction, Env: "prod", Apps: []provider.LiveApp{{App: "web", Compute: provider.ComputeContainer, Workers: workers}}}
}

func TestAContainerAppHostingWorkersKeepsItsWorkersStackAcrossPromotions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, engine, spec := containerStacksHostingWorkers(t, fake.NewKeyValues())
	for deploy := range 2 {
		if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
			t.Fatalf("Provision() of deploy %d = %v", deploy+1, err)
		}
		plan, err := stacks.PlanWorkersRetirement(ctx, liveContainerApp("media"), nil)
		if err != nil {
			t.Fatalf("PlanWorkersRetirement() of deploy %d = %v", deploy+1, err)
		}
		if len(plan.Groups) != 0 {
			t.Errorf("the retirement plan of deploy %d = %v, want nothing: web still hosts its workers on containers", deploy+1, plan.Groups)
		}
		if err := stacks.RetireWorkers(ctx, liveContainerApp("media"), progress.Discard()); err != nil {
			t.Fatalf("RetireWorkers() of deploy %d = %v", deploy+1, err)
		}
	}

	if workers := naming.ContainerWorkersStack("prod", "web").String(); slices.Contains(engine.torn(), workers) {
		t.Errorf("took down %v, want the workers stack %s the app still hosts kept", engine.torn(), workers)
	}
}

func TestAContainerAppThatDropsItsWorkersLosesTheirStackOnlyOnceThePromotionServes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() with workers = %v", err)
	}
	dropped := spec
	app := *spec.App
	app.Workers, app.DeploymentWorkers, app.Topics = nil, nil, nil
	dropped.App = &app
	dropped.Ref.Name = naming.AppStack("prod", "web", mustReleaseToken(t, "r00000009"))
	engine.previewed = []provider.Change{{Kind: "aws:ecs/service:Service", Name: "media"}}
	workers := naming.ContainerWorkersStack("prod", "web").String()

	plan, err := stacks.Plan(ctx, dropped, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() without workers = %v", err)
	}
	if planGroupFor(plan.Groups, workers, provider.ActionDelete) {
		t.Errorf("the app's plan deletes %s, want it left to the retirement after the promotion", workers)
	}
	if _, err := stacks.Provision(ctx, dropped, progress.Discard()); err != nil {
		t.Fatalf("Provision() without workers = %v", err)
	}
	if slices.Contains(engine.torn(), workers) {
		t.Fatalf("took down %v before the promotion, so a deploy that fails now leaves the live release's tasks unserved", engine.torn())
	}

	retirement, err := stacks.PlanWorkersRetirement(ctx, liveContainerApp(), nil)
	if err != nil {
		t.Fatalf("PlanWorkersRetirement() = %v", err)
	}
	if !planGroupFor(retirement.Groups, workers, provider.ActionDelete) {
		t.Errorf("the retirement plan = %v, want %s deleted", retirement.Groups, workers)
	}
	if err := stacks.RetireWorkers(ctx, liveContainerApp(), progress.Discard()); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}
	if !slices.Contains(engine.torn(), workers) {
		t.Errorf("took down %v once the promotion served, want %s", engine.torn(), workers)
	}
	consumers, err := listConsumers(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(consumers, func(consumer records.Entry) bool { return strings.Contains(consumer.Key.String(), workers) }) {
		t.Errorf("container infrastructure consumers = %v, want the retired workers stack's hold let go", consumers)
	}
}

func TestMovingAnAppFromFunctionsToContainersRetiresItsLambdaWorkersOnlyOnceThePromotionServes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	lambda := workersRef(spec.Ref)
	recordWorkersStack(t, store, lambda)

	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() on containers = %v", err)
	}
	containers := naming.ContainerWorkersStack("prod", "web").String()
	if slices.Contains(engine.torn(), lambda.Name.String()) || slices.Contains(engine.stacks(), lambda.Name.String()) {
		t.Errorf("ran %v and took down %v before the promotion, want the Lambda workers stack %s left alone while the release on functions serves", engine.stacks(), engine.torn(), lambda.Name)
	}
	if !slices.Contains(engine.stacks(), containers) {
		t.Errorf("ran %v, want the container workers stack %s", engine.stacks(), containers)
	}

	if err := stacks.RetireWorkers(ctx, liveContainerApp("media"), progress.Discard()); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}
	if got := engine.torn(); !slices.Equal(got, []string{lambda.Name.String()}) {
		t.Errorf("took down %v once the promotion served, want the Lambda workers stack %s alone", got, lambda.Name)
	}
}

func TestMovingAnAppFromContainersToFunctionsRetiresItsContainerWorkersOnlyOnceThePromotionServes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() on containers = %v", err)
	}
	lambda := workersRef(spec.Ref)
	recordWorkersStack(t, store, lambda)
	onFunctions := provider.LiveApps{Slug: spec.Ref.Project, Tier: environment.TierProduction, Env: "prod", Apps: []provider.LiveApp{{App: "web", Compute: provider.ComputeServerless, Workers: []string{"media"}}}}

	plan, err := stacks.PlanWorkersRetirement(ctx, onFunctions, nil)
	if err != nil {
		t.Fatalf("PlanWorkersRetirement() = %v", err)
	}
	containers := naming.ContainerWorkersStack("prod", "web").String()
	var planned []string
	for _, group := range plan.Groups {
		planned = append(planned, group.Name)
	}
	if !slices.Equal(planned, []string{containers}) {
		t.Errorf("the retirement plan deletes %v, want %s alone", planned, containers)
	}
	if err := stacks.RetireWorkers(ctx, onFunctions, progress.Discard()); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}
	if got := engine.torn(); !slices.Equal(got, []string{containers}) {
		t.Errorf("took down %v, want the container workers stack %s alone", got, containers)
	}
	if got := countRecordedWorkerStacks(t, store, spec.Ref); got != 1 {
		t.Errorf("%d workers stacks are recorded, want the Lambda one alone", got)
	}
}

func TestTheContainersPartIsKeptWhileAContainerWorkersStackRunsInItsCluster(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if err := stacks.containers.Release(ctx, store, spec.Ref, progress.Discard()); err != nil {
		t.Fatalf("let go of the app's own hold: %v", err)
	}

	part, present, err := containers.ReadPart(ctx, store, "us-east-1", environment.TierProduction)
	if err != nil || !present {
		t.Fatalf("containers.ReadPart() = %v, %v, want the containers part while the workers' services run in its cluster", present, err)
	}
	if !slices.Equal(part.UsedBy, []string{spec.Ref.Project}) {
		t.Errorf("containers used by %v, want %s, whose workers run there", part.UsedBy, spec.Ref.Project)
	}
	if err := stacks.containers.Remove(ctx, environment.TierProduction, progress.Discard()); err == nil {
		t.Error("containers.Remove() = nil, want it refused while workers services run in the cluster")
	}
	if infra := containers.StackRef(environment.TierProduction).Name.String(); slices.Contains(engine.torn(), infra) {
		t.Fatalf("took down %v, want the shared infrastructure kept", engine.torn())
	}

	if err := stacks.RetireWorkers(ctx, provider.LiveApps{Slug: spec.Ref.Project, Tier: environment.TierProduction, Env: "prod"}, progress.Discard()); err != nil {
		t.Fatalf("RetireWorkers() = %v", err)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, containers.Part); err != nil || present {
		t.Errorf("containers record present %v (%v) after the last workers stack went, want it forgotten with the infrastructure", present, err)
	}
}

func planGroupFor(groups []provider.ChangeGroup, stack string, action provider.ChangeAction) bool {
	return slices.ContainsFunc(groups, func(group provider.ChangeGroup) bool {
		return group.Kind == provider.StackGroupKind && group.Name == stack && group.Action == action
	})
}

func TestPlanningAContainerAppHostingWorkersIncludesTheirStack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stacks, _, spec := containerStacksHostingWorkers(t, fake.NewKeyValues())
	workers := naming.ContainerWorkersStack("prod", "web").String()

	first, err := stacks.Plan(ctx, spec, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() before the cluster exists = %v", err)
	}
	if !planGroupFor(first.Groups, workers, provider.ActionCreate) {
		t.Errorf("plan groups before the cluster exists = %v, want the workers stack %s created", first.Groups, workers)
	}

	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	next, err := stacks.Plan(ctx, spec, progress.Discard())
	if err != nil {
		t.Fatalf("Plan() once the cluster exists = %v", err)
	}
	if !slices.ContainsFunc(next.Groups, func(group provider.ChangeGroup) bool { return group.Name == workers }) {
		t.Errorf("plan groups = %v, want the workers stack %s the deploy provisions", next.Groups, workers)
	}
}

func TestReclaimingAFailedDeploysContainerReleaseLeavesTheContainerWorkersTheLiveReleaseRuns(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() of the live release = %v", err)
	}
	recordRelease(t, store, spec.Ref)
	failed := spec
	failed.Ref.Name = naming.AppStack("prod", "web", mustReleaseToken(t, "r00000009"))
	if _, err := stacks.Provision(ctx, failed, progress.Discard()); err != nil {
		t.Fatalf("Provision() of the failing deploy's release = %v", err)
	}
	recordRelease(t, store, failed.Ref)

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: failed.Ref}, progress.Discard()); err != nil {
		t.Fatalf("Destroy(the failed deploy's release) = %v", err)
	}

	workers := naming.ContainerWorkersStack("prod", "web")
	if slices.Contains(engine.torn(), workers.String()) {
		t.Errorf("took down %v on reclaiming a release that never went live, so the live release's tasks stop being served", engine.torn())
	}
	consumers, err := listConsumers(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(consumers, func(consumer records.Entry) bool { return strings.Contains(consumer.Key.String(), workers.String()) }) {
		t.Errorf("container infrastructure consumers = %v, want the live workers stack still holding the cluster", consumers)
	}
}

func TestThePlanToDestroyTheLastContainerReleaseListsItsContainerWorkers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	recordRelease(t, store, spec.Ref)
	engine.previewed = []provider.Change{{Kind: "aws:ecs/service:Service", Name: "media"}}

	plan, err := stacks.PlanDestroy(ctx, spec.Ref, progress.Discard())
	if err != nil {
		t.Fatalf("PlanDestroy() = %v", err)
	}
	if workers := naming.ContainerWorkersStack("prod", "web").String(); !planGroupFor(plan.Groups, workers, provider.ActionDelete) {
		t.Errorf("plan groups = %v, want the container workers stack %s deleted with the last release", plan.Groups, workers)
	}
}

var webAsItsOwnWorker = provider.WorkerSpec{Name: "web", Concurrency: 2, App: "web", Compute: provider.ComputeContainer}

func TestAContainerAppThatOnlyHostsItsWorkerDeclaresNoWebServiceOfItsOwn(t *testing.T) {
	t.Parallel()

	opened, spec, _ := hostingContainerWorkers(t, webAsItsOwnWorker)
	work, err := opened.containerWork(spec, fixtureContainerInfra())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", "prod--web", rec)); err != nil {
		t.Fatalf("run the app's program: %v", err)
	}

	for _, kind := range []string{"aws:ecs/service:Service", "aws:ecs/taskDefinition:TaskDefinition", "aws:lb/targetGroup:TargetGroup", "aws:lb/listenerRule:ListenerRule"} {
		if declared := rec.registered(kind); len(declared) > 0 {
			t.Errorf("declared %s %v, want none: a worker app takes no traffic, so a web service would only bill its floor idle", kind, declared)
		}
	}
}

func TestProvisioningAContainerAppThatOnlyHostsItsWorkerReturnsTheWorkerAloneAndUsesTheContainersPart(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
	stacks, engine, spec := containerStacksHostingWorkers(t, store)
	spec.App.Workers = []provider.WorkerSpec{webAsItsOwnWorker}
	spec.App.DeploymentWorkers = spec.App.Workers
	spec.App.Topics = []provider.Resource{taskResource("resize", provider.ConsumerSpec{Worker: "web"})}
	delete(engine.outputs, "web")
	engine.outputs[resources.WorkerName("web")] = auto.OutputValue{Value: map[string]any{outputKeyContainerPhysical: containerWorkerService("shop", "prod", "web", "web")}}

	result, err := stacks.Provision(context.Background(), spec, progress.Discard())
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Containers) != 1 || result.Containers[0].Name != resources.WorkerName("web") || result.Containers[0].URL != "" {
		t.Errorf("Containers = %+v, want the worker alone, with no URL", result.Containers)
	}
	part, present, err := containers.ReadPart(context.Background(), store, "us-east-1", environment.TierProduction)
	if err != nil || !present || !slices.Equal(part.UsedBy, []string{spec.Ref.Project}) {
		t.Errorf("containers part = %+v, %v, %v, want it added and used by %s: its worker's service runs in the cluster", part, present, err, spec.Ref.Project)
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
	opened := &release{cfg: Config{Region: "us-east-1", StateTableARN: testStateTableARN}}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error {
		return declareTopicsAndTasks(pctx, opened, "shop", "prod", []provider.Resource{nightly})
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
