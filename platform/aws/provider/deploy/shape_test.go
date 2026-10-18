package deploy

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/pricing"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/provider/transform/transformtest"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	awsresources "github.com/ocelhq/ocel/platform/aws/provider/resources"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/kv"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/realtime"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/topic"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/variables"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/functionnetwork"
)

var pulumiTokens = map[string]string{
	"aws:lambda/function:Function":            "aws_lambda_function",
	"aws:lambda/functionUrl:FunctionUrl":      "aws_lambda_function_url",
	"aws:cloudwatch/logGroup:LogGroup":        "aws_cloudwatch_log_group",
	"aws:s3/bucketV2:BucketV2":                "aws_s3_bucket",
	"aws:rds/cluster:Cluster":                 "aws_rds_cluster",
	"aws:rds/clusterInstance:ClusterInstance": "aws_rds_cluster_instance",
	"aws:ecs/cluster:Cluster":                 "aws_ecs_cluster",
	"aws:ecs/taskDefinition:TaskDefinition":   "aws_ecs_task_definition",
	"aws:ecs/service:Service":                 "aws_ecs_service",
	"aws:lb/loadBalancer:LoadBalancer":        "aws_lb",
	"aws:ec2/natGateway:NatGateway":           "aws_nat_gateway",
	"aws:ec2/eip:Eip":                         "aws_eip",

	"aws:elasticache/replicationGroup:ReplicationGroup": "aws_elasticache_replication_group",
	"aws:elasticache/parameterGroup:ParameterGroup":     "aws_elasticache_parameter_group",
	"aws:elasticache/subnetGroup:SubnetGroup":           "aws_elasticache_subnet_group",

	"aws:cloudwatch/metricAlarm:MetricAlarm": "aws_cloudwatch_metric_alarm",

	"aws:sqs/queue:Queue":                              "aws_sqs_queue",
	"aws:sns/topic:Topic":                              "aws_sns_topic",
	"aws:lambda/eventSourceMapping:EventSourceMapping": "aws_lambda_event_source_mapping",
	"aws:scheduler/schedule:Schedule":                  "aws_scheduler_schedule",
	"aws:appsync/api:Api":                              "aws_appsync_api",
}

func shapeRequest(t *testing.T) provider.ShapeRequest {
	t.Helper()
	release := fixedRelease(t)
	return provider.ShapeRequest{
		Deploy: provider.DeploySpec{
			Slug:  "shop",
			Tier:  environment.TierProduction,
			Env:   "prod",
			Infra: naming.InfraStack("prod"),
			Apps: []provider.AppEntry{
				{App: "web", Stack: naming.AppStack("prod", "web", release), Manifest: &contractv1.ManifestApp{Name: "web", Framework: &contractv1.Framework{Name: "next"}, Artifact: &contractv1.ManifestApp_Serverless{Serverless: &contractv1.ServerlessArtifact{}}}, Workers: []provider.WorkerSpec{{Name: "worker"}, {Name: "ledger"}}},
				{App: "api", Stack: naming.AppStack("prod", "api", release), Manifest: &contractv1.ManifestApp{Name: "api", Framework: &contractv1.Framework{Name: "go"}, Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{}}}, Instances: provider.Instances{Min: 1, Max: 4}, Workers: []provider.WorkerSpec{{Name: "media", App: "api", Compute: provider.ComputeContainer}}},
			},
		},
		Resources: []provider.Resource{
			taskResource("transcode", provider.ConsumerSpec{Worker: "media"}),
			topicResource("uploaded", provider.ConsumerSpec{Name: "thumbnails", Worker: "media"}, provider.ConsumerSpec{Name: "captions", Worker: "media"}),
			{Name: "main", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}},
			{Name: "uploads", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}},
			{Name: "shared", Type: provider.BindingBucket, Binding: "elsewhere"},
			{Name: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{}},
			{Name: "realtime--live", Declared: "live", Type: provider.BindingRealtime, Realtime: &provider.RealtimeSpec{}},
			{Name: "realtime--chat", Declared: "chat", Type: provider.BindingRealtime, Realtime: &provider.RealtimeSpec{}},
			taskResource("heartbeat", provider.ConsumerSpec{Worker: "worker"}, cronEveryMinute),
			topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}, provider.ConsumerSpec{Name: "ledger-entry", Worker: "ledger"}),
		},
		Functions: map[string][]provider.FunctionSpec{
			"web": {{Name: "fn--web--entry"}, {Name: "fn--web--admin", Route: "/admin"}},
		},
		Usages: []provider.AppUsage{{App: "web", Resources: []provider.Resource{{Name: "main", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}}}}},
	}
}

func shaped(t *testing.T, pass transform.Pass, req provider.ShapeRequest) *costv1.ResourceSet {
	t.Helper()
	tree := &pricing.Tree{}
	project := tree.Scope("", "project", "shop")
	scopes := ShapeScopes{
		Shared:      tree.Scope(project, "shared", "production"),
		Environment: tree.Scope(project, "environment", "prod"),
	}
	if err := Shape(context.Background(), pass, "us-east-1", req, tree, scopes); err != nil {
		t.Fatalf("Shape() = %v", err)
	}
	set, err := tree.Set("ocel")
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func countTypes(set *costv1.ResourceSet) map[string]int {
	counts := map[string]int{}
	for _, r := range set.GetResources() {
		counts[r.GetType()]++
	}
	return counts
}

func shapedNamed(t *testing.T, set *costv1.ResourceSet, typ, name string) map[string]any {
	t.Helper()
	for _, r := range set.GetResources() {
		if r.GetType() == typ && r.GetName() == name {
			return r.GetProperties().AsMap()
		}
	}
	t.Fatalf("no %s named %s among %v", typ, name, set.GetResources())
	return nil
}

type secretedCluster struct{ *inputRecorder }

func (s secretedCluster) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	id, state, err := s.inputRecorder.NewResource(args)
	if secrets := masterUserSecret(args); secrets != nil {
		state = state.Copy()
		state["masterUserSecrets"] = secrets["masterUserSecrets"]
	}
	return id, state, err
}

func (s secretedCluster) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	return (&functionNetworkCloud{inputRecorder: s.inputRecorder, vpcCIDR: "172.31.0.0/16", subnets: defaultVPCSubnets()}).Call(args)
}

func everyProgramRecorded(t *testing.T) *inputRecorder {
	t.Helper()
	rec := &inputRecorder{}
	run := func(name string, program func(*pulumi.Context) error) {
		if err := pulumi.RunErr(program, pulumi.WithMocks("shop", name, secretedCluster{rec})); err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
	}
	cfg, appSpec := appStackSpec(t)
	appWork, err := releasing(t, cfg).appWork(appSpec, nil, nil, variables.Bundle{}, stagedOf(appSpec))
	if err != nil {
		t.Fatal(err)
	}
	run("app", func(pctx *pulumi.Context) error { return appWork.run(pctx, nil) })

	containerCfg, containerSpec := containerStackSpec(t)
	containerWork, err := releasing(t, containerCfg).containerWork(containerSpec, fixtureContainerInfra())
	if err != nil {
		t.Fatal(err)
	}
	run("container", containerWork.run)
	run("function-network", functionnetwork.NewProgram(environment.TierProduction, "us-east-1", &functionNetworkCloud{subnets: defaultVPCSubnets()}).Run)
	run("container-infra", containers.NewProgram(environment.TierProduction, containerCfg.AppBoundaryARN, containerCfg.AppRolePath, nil).Run)
	topicsRelease := &release{cfg: Config{Region: "us-east-1", StateTableARN: testStateTableARN}}
	req := shapeRequest(t)
	run("topics", func(pctx *pulumi.Context) error {
		return declareTopicsAndTasks(pctx, topicsRelease, "shop", "prod", req.Resources)
	})
	hosted := &workersWork{
		project: "shop", stack: workersStack("prod", "web"), topics: topic.Of("shop", "prod", req.Resources), workers: req.Deploy.Apps[0].Workers,
		args: functionArgs{Runtime: defaultFunctionRuntime, Arch: "arm64", MemorySizeMB: 1024}, role: executionRole{App: "web", Boundary: componenttest.BoundaryARN, Path: componenttest.AppRolePath},
		region: "us-east-1", account: mockAccount, table: testStateTableARN, prefix: naming.TaskKeyPrefix("shop", "prod"), group: queues.ScheduleGroupName("shop", "prod"),
	}
	run("workers", hosted.run)
	jobsCfg, jobsSpec := containerStackSpec(t)
	jobsCfg.StateTable, jobsCfg.StateTableARN = "ocel-state", testStateTableARN
	jobsSpec.App.Instances = provider.Instances{Min: 1, Max: 4}
	jobsSpec.App.Workers = req.Deploy.Apps[1].Workers
	jobsSpec.App.DeploymentWorkers = jobsSpec.App.Workers
	jobsSpec.App.Topics = req.Resources
	jobsRelease := releasing(t, jobsCfg)
	jobsWeb, err := jobsRelease.containerWork(jobsSpec, fixtureContainerInfra())
	if err != nil {
		t.Fatal(err)
	}
	containerWorkers, err := jobsRelease.containerWorkersWork(jobsSpec, jobsWeb)
	if err != nil {
		t.Fatal(err)
	}
	run("container-workers", containerWorkers.run)
	run(naming.InfraApp, func(pctx *pulumi.Context) error {
		registry := awsresources.Registry(awsresources.Clients{
			Artifacts:   component.Artifacts{Objects: &fakeArtifactStore{exists: map[string]bool{}}, Bucket: "ocel-artifacts"},
			KVTokens:    kv.Tokens{Parameters: newFakeParameters(), Root: "/ocel/kv"},
			SigningKeys: realtime.Keys{SigningKeys: newFakeSigningKeys(), Root: "/ocel/realtime"},
		})
		at := component.Stack{
			Ref:     provider.StackRef{Tier: environment.TierProduction, Project: "shop"},
			Project: "shop", Env: "prod", StateTable: "ocel-state", StateTableARN: testStateTableARN,
			AppBoundaryARN: containerCfg.AppBoundaryARN, AppRolePath: containerCfg.AppRolePath,
			Network:    component.Network{VPCID: "vpc-1", VPCCIDR: "10.0.0.0/16", SubnetIDs: []string{"subnet-a"}},
			Transforms: (*transformPatches)(nil),
			Resources:  req.Resources,
		}
		for _, declaring := range registry.Components() {
			if slices.Contains(declaring.Types, provider.BindingTopic) || slices.Contains(declaring.Types, provider.BindingTask) {
				continue
			}
			if err := declaring.Declare(pctx, at, declaring.Declared(req.Resources)); err != nil {
				return err
			}
		}
		return nil
	})

	return rec
}

func TestShapeRegistersWhatTheProgramsRegister(t *testing.T) {
	t.Parallel()

	set := shaped(t, nil, shapeRequest(t))

	rec := everyProgramRecorded(t)
	registered := map[string]int{}
	for key := range rec.recorded {
		token, _, _ := strings.Cut(key, "::")
		if tf, billable := pulumiTokens[token]; billable {
			registered[tf]++
		}
	}
	got := countTypes(set)
	delete(got, "aws_secretsmanager_secret")
	delete(got, "aws_ecr_repository")
	delete(got, "aws_ssm_parameter")
	if !maps.Equal(got, registered) {
		t.Errorf("shape counts %v, the programs register %v", got, registered)
	}

	entry := recordedOf(t, rec, "aws:lambda/function:Function")
	for key := range rec.recorded {
		if strings.HasPrefix(key, "aws:lambda/function:Function::shop-prod-web") {
			entry = rec.recorded[key]
		}
	}
	lambda := shapedNamed(t, set, "aws_lambda_function", "fn--web--entry")
	if lambda["memory_size"] != entry["memorySize"].NumberValue() || lambda["timeout"] != entry["timeout"].NumberValue() {
		t.Errorf("shaped lambda = %v, program registered memory %v timeout %v", lambda, entry["memorySize"], entry["timeout"])
	}
	if arch := lambda["architectures"].([]any); arch[0] != entry["architectures"].ArrayValue()[0].StringValue() {
		t.Errorf("shaped architectures = %v, program registered %v", arch, entry["architectures"])
	}
	task := recordedOf(t, rec, "aws:ecs/taskDefinition:TaskDefinition")
	definition := shapedNamed(t, set, "aws_ecs_task_definition", "api")
	if definition["cpu"] != task["cpu"].StringValue() || definition["memory"] != task["memory"].StringValue() {
		t.Errorf("shaped task = %v, program registered cpu %v memory %v", definition, task["cpu"], task["memory"])
	}
	cluster := recordedOf(t, rec, "aws:rds/cluster:Cluster")
	scaling := shapedNamed(t, set, "aws_rds_cluster", "main")["serverlessv2_scaling_configuration"].(map[string]any)
	registeredScaling := cluster["serverlessv2ScalingConfiguration"].ObjectValue()
	if scaling["min_capacity"] != registeredScaling["minCapacity"].NumberValue() || scaling["max_capacity"] != registeredScaling["maxCapacity"].NumberValue() {
		t.Errorf("shaped scaling = %v, program registered %v", scaling, registeredScaling)
	}
	group := recordedOf(t, rec, "aws:elasticache/replicationGroup:ReplicationGroup")
	store := shapedNamed(t, set, "aws_elasticache_replication_group", "cache")
	if store["node_type"] != group["nodeType"].StringValue() || store["num_cache_clusters"] != group["numCacheClusters"].NumberValue() {
		t.Errorf("shaped store = %v, program registered node %v times %v", store, group["nodeType"], group["numCacheClusters"])
	}
	if token := shapedNamed(t, set, "aws_ssm_parameter", "cache"); token["type"] != "SecureString" {
		t.Errorf("shaped token = %v, want the SecureString the store's AUTH token is kept in", token)
	}
	authorizer := rec.recorded["aws:lambda/function:Function::realtime-authorizer"]
	shapedAuthorizer := shapedNamed(t, set, "aws_lambda_function", "realtime-authorizer")
	if shapedAuthorizer["memory_size"] != authorizer["memorySize"].NumberValue() || shapedAuthorizer["timeout"] != authorizer["timeout"].NumberValue() || shapedAuthorizer["architectures"].([]any)[0] != authorizer["architectures"].ArrayValue()[0].StringValue() {
		t.Errorf("shaped authorizer = %v, program registered %v", shapedAuthorizer, authorizer)
	}
	for _, name := range []string{"live", "chat"} {
		if key := shapedNamed(t, set, "aws_secretsmanager_secret", name); key["managed_by"] != "ocel" {
			t.Errorf("shaped signing key %s = %v, want the secret a deploy mints for it", name, key)
		}
	}
}

func TestEveryRoleTheProgramsMintSitsUnderTheTiersAppRolePath(t *testing.T) {
	t.Parallel()

	rec := everyProgramRecorded(t)
	roles := rec.registered("aws:iam/role:Role")
	if len(roles) == 0 {
		t.Fatal("the programs registered no role")
	}
	for _, name := range roles {
		path := rec.recorded["aws:iam/role:Role::"+name]["path"]
		if !path.IsString() || path.StringValue() != componenttest.AppRolePath {
			t.Errorf("role %s has path %v, want %q: a credential passes an app role only under its own tier's path", name, path, componenttest.AppRolePath)
		}
	}
}

func TestShapeScopesAppsUnderTheEnvironmentAndTheContainerInfraAsShared(t *testing.T) {
	t.Parallel()

	set := shaped(t, nil, shapeRequest(t))

	scopeOf := map[string]string{}
	for _, r := range set.GetResources() {
		scopeOf[r.GetType()+":"+r.GetName()] = r.GetScope()
	}
	want := map[string]string{
		"aws_lambda_function:fn--web--entry": "project:shop/environment:prod/app:web",
		"aws_ecs_service:api":                "project:shop/environment:prod/app:api",
		"aws_lb:ocel-containers":             "project:shop/shared:production",
		"aws_rds_cluster:main":               "project:shop/environment:prod",
		"aws_s3_bucket:uploads":              "project:shop/environment:prod",
	}
	for key, scope := range want {
		if scopeOf[key] != scope {
			t.Errorf("%s in scope %q, want %q", key, scopeOf[key], scope)
		}
	}
	if _, bound := scopeOf["aws_s3_bucket:shared"]; bound {
		t.Error("a bound resource is someone else's bill, and was shaped anyway")
	}
	if n := len(set.GetScopes()); n != 5 {
		t.Errorf("scopes = %d, want project, shared, environment and two apps", n)
	}
	if set.GetResources()[0].GetRegion() != "us-east-1" {
		t.Errorf("region = %q", set.GetResources()[0].GetRegion())
	}
}

func TestAContainerAppIsBilledForTheTasksItsFloorKeepsRunning(t *testing.T) {
	t.Parallel()

	req := shapeRequest(t)
	req.Deploy.Apps[1].Instances = provider.Instances{Min: 3, Max: 8}
	set := shaped(t, nil, req)

	if count := shapedNamed(t, set, "aws_ecs_service", "api")["desired_count"]; count != float64(3) {
		t.Errorf("the service is shaped at %v tasks, want the 3 its floor keeps running", count)
	}
}

func TestAnAppWithNoBuildShapesOneFunction(t *testing.T) {
	t.Parallel()

	req := shapeRequest(t)
	req.Functions, req.Deploy.Apps[0].Workers = nil, nil
	set := shaped(t, nil, req)

	if got := countTypes(set)["aws_lambda_function"]; got != 3 {
		t.Errorf("lambdas = %d, want one for web, the upload completer and the realtime authorizer", got)
	}
	if memory := shapedNamed(t, set, "aws_lambda_function", "web")["memory_size"]; memory != float64(nextBundleFunctionMemoryMB) {
		t.Errorf("a next app's stand-in function has %v MB, want %d", memory, nextBundleFunctionMemoryMB)
	}
}

const sizingModule = `
	import { defineTransform } from "@ocel/transforms"
	export default defineTransform(({ bindings }) => ({
		aws: {
			function: { lambda: { memorySize: 2048, vpcConfig: { subnetIds: bindings.custom.network.subnetIds, securityGroupIds: bindings.custom.network.securityGroupIds } } },
			postgres: { cluster: { serverlessv2ScalingConfiguration: { minCapacity: 0.5, maxCapacity: 8 } } },
			kv: { replicationGroup: { numCacheClusters: 1, automaticFailoverEnabled: false, multiAzEnabled: false } },
		},
	}))
`

func TestTransformsResizeTheShapeAndBindingOutputsStayUnknown(t *testing.T) {
	t.Parallel()

	root := transformtest.Root(t, map[string]string{"sizing.transform.ts": sizingModule})
	pass := NodePass(root, []string{"./sizing.transform.ts"})
	set := shaped(t, pass, shapeRequest(t))

	lambda := shapedNamed(t, set, "aws_lambda_function", "fn--web--entry")
	if lambda["memory_size"] != float64(2048) {
		t.Errorf("memory_size = %v, want the transform's 2048", lambda["memory_size"])
	}
	scaling := shapedNamed(t, set, "aws_rds_cluster", "main")["serverlessv2_scaling_configuration"].(map[string]any)
	if scaling["min_capacity"] != 0.5 || scaling["max_capacity"] != float64(8) {
		t.Errorf("scaling = %v, want the transform's 0.5 to 8", scaling)
	}
	if nodes := shapedNamed(t, set, "aws_elasticache_replication_group", "cache")["num_cache_clusters"]; nodes != float64(1) {
		t.Errorf("num_cache_clusters = %v, want the transform's single node", nodes)
	}
	for _, r := range set.GetResources() {
		if r.GetType() == "aws_lambda_function" && r.GetName() == "fn--web--entry" {
			if !slices.Contains(r.GetUnknown(), "vpc_config") {
				t.Errorf("unknown = %v, want vpc_config, which a binding fills at deploy", r.GetUnknown())
			}
		}
	}
}

func TestAContainerWorkerIsShapedAtNoTasksWithOneAlarmForEachNineQueuesItWatches(t *testing.T) {
	t.Parallel()

	req := shapeRequest(t)
	set := shaped(t, nil, req)

	service := shapedNamed(t, set, "aws_ecs_service", resources.WorkerName("media"))
	if service["desired_count"] != float64(0) {
		t.Errorf("the worker's service is shaped at %v tasks, want none: it is billed for what it runs, and idle it runs nothing", service["desired_count"])
	}
	alarm := shapedNamed(t, set, "aws_cloudwatch_metric_alarm", resources.WorkerName("media")+"/0")
	if alarm["metrics"] != float64(3) {
		t.Errorf("the worker's alarm is shaped over %v metrics, want the 3 queues of its transcode task and uploaded topic", alarm["metrics"])
	}
	scopeOf := map[string]string{}
	for _, r := range set.GetResources() {
		scopeOf[r.GetType()+":"+r.GetName()] = r.GetScope()
	}
	if scope := scopeOf["aws_ecs_service:"+resources.WorkerName("media")]; scope != "project:shop/environment:prod/app:api" {
		t.Errorf("the worker is billed in scope %q, want the app that hosts it", scope)
	}
}

func TestAnEphemeralPreviewShapesTheContainerWorkersItDeclares(t *testing.T) {
	t.Parallel()

	req := shapeRequest(t)
	req.Deploy.Tier = environment.TierPreview
	req.Deploy.Env = "pr-7"
	req.Deploy.Infra = naming.InfraStack("pr-7")
	set := shaped(t, nil, req)

	service := shapedNamed(t, set, "aws_ecs_service", resources.WorkerName("media"))
	if service["desired_count"] != float64(0) {
		t.Errorf("an ephemeral preview shapes its worker's service at %v tasks, want none, as a persistent environment does", service["desired_count"])
	}
	shapedNamed(t, set, "aws_cloudwatch_metric_alarm", resources.WorkerName("media")+"/0")
}

func TestAContainerWorkerAppIsShapedWithItsWorkersAtItsFloorAndNoWebService(t *testing.T) {
	t.Parallel()

	req := shapeRequest(t)
	for i, app := range req.Deploy.Apps {
		if app.App == "api" {
			req.Deploy.Apps[i].Instances = provider.Instances{Min: 2, Max: 4}
			req.Deploy.Apps[i].Workers = []provider.WorkerSpec{{Name: "api", App: "api", Compute: provider.ComputeContainer}}
		}
	}
	req.Resources = append(req.Resources, taskResource("encode", provider.ConsumerSpec{Worker: "api"}))
	set := shaped(t, nil, req)

	for _, r := range set.GetResources() {
		if (r.GetType() == "aws_ecs_service" || r.GetType() == "aws_ecs_task_definition") && r.GetName() == "api" {
			t.Errorf("shaped %s %s, want no web service for an app only its worker runs on", r.GetType(), r.GetName())
		}
	}
	if service := shapedNamed(t, set, "aws_ecs_service", resources.WorkerName("api")); service["desired_count"] != float64(2) {
		t.Errorf("the worker's service is shaped at %v tasks, want the app's floor of 2", service["desired_count"])
	}
}

func TestACronTaskOnAContainerWorkerIsShapedWithItsSchedule(t *testing.T) {
	t.Parallel()

	req := shapeRequest(t)
	req.Resources = append(req.Resources, taskResource("nightly", provider.ConsumerSpec{Worker: "media"}, cronEveryMinute))
	set := shaped(t, nil, req)

	if schedule := shapedNamed(t, set, "aws_scheduler_schedule", "nightly"); schedule["schedule_expression"] == nil {
		t.Errorf("the nightly schedule is shaped with no expression: %v", schedule)
	}
}
