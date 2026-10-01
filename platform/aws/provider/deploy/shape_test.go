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
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/pkg/provider/transform/transformtest"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
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

	"aws:elasticache/replicationGroup:ReplicationGroup": "aws_elasticache_replication_group",
	"aws:elasticache/parameterGroup:ParameterGroup":     "aws_elasticache_parameter_group",
	"aws:elasticache/subnetGroup:SubnetGroup":           "aws_elasticache_subnet_group",

	"aws:sqs/queue:Queue":                              "aws_sqs_queue",
	"aws:sns/topic:Topic":                              "aws_sns_topic",
	"aws:lambda/eventSourceMapping:EventSourceMapping": "aws_lambda_event_source_mapping",
	"aws:scheduler/schedule:Schedule":                  "aws_scheduler_schedule",
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
				{App: "api", Stack: naming.AppStack("prod", "api", release), Manifest: &contractv1.ManifestApp{Name: "api", Framework: &contractv1.Framework{Name: "go"}, Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{}}}, Instances: provider.Instances{Min: 1, Max: 1}},
			},
		},
		Resources: []provider.Resource{
			{Name: "main", Type: provider.BindingPostgres, Postgres: &provider.PostgresSpec{}},
			{Name: "uploads", Type: provider.BindingBucket, Bucket: &provider.BucketSpec{}},
			{Name: "shared", Type: provider.BindingBucket, Binding: "elsewhere"},
			{Name: "cache", Type: provider.BindingKV, KV: &provider.KVSpec{}},
			taskResource("heartbeat", provider.ConsumerSpec{Worker: "worker"}, cronEveryMinute),
			topicResource("orders", provider.ConsumerSpec{Name: "audit-log", Worker: "worker"}, provider.ConsumerSpec{Name: "ledger-entry", Worker: "ledger"}),
		},
		Functions: map[string][]provider.FunctionSpec{
			"web": {{Name: "fn--web--entry"}, {Name: "fn--web--admin", Route: "/admin"}},
		},
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

func TestShapeRegistersWhatTheProgramsRegister(t *testing.T) {
	t.Parallel()

	set := shaped(t, nil, shapeRequest(t))

	rec := &inputRecorder{}
	run := func(name string, program func(*pulumi.Context) error) {
		if err := pulumi.RunErr(program, pulumi.WithMocks("shop", name, secretedCluster{rec})); err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
	}
	cfg, appSpec := appStackSpec(t)
	appWork, err := releasing(t, cfg).appWork(appSpec, nil)
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
	run("container-infra", (&containerInfraWork{tier: environment.TierProduction, boundary: containerCfg.AppBoundaryARN}).run)
	topicsRelease := &release{cfg: Config{Region: "us-east-1", StateTableARN: testStateTableARN}}
	req := shapeRequest(t)
	run("topics", func(pctx *pulumi.Context) error {
		return topicsRelease.declareTopics(pctx, "shop", "prod", req.Resources)
	})
	hosted := &workersWork{
		project: "shop", stack: workersStack("prod", "web"), topics: topicsOf("shop", "prod", req.Resources), workers: req.Deploy.Apps[0].Workers,
		args: functionArgs{Runtime: defaultFunctionRuntime, Arch: "arm64", MemorySizeMB: 1024}, role: executionRole{App: "web", Boundary: testBoundaryARN},
		region: "us-east-1", account: mockAccount, table: testStateTableARN, prefix: naming.TaskKeyPrefix("shop", "prod"), group: queues.ScheduleGroupName("shop", "prod"),
	}
	run("workers", hosted.run)
	run(naming.InfraApp, func(pctx *pulumi.Context) error {
		if err := registerPostgres(pctx, "shop", "prod", "main", translatePostgres(nil), "vpc-1", "10.0.0.0/16", []string{"subnet-a"}); err != nil {
			return err
		}
		store, err := translateKV("cache", nil)
		if err != nil {
			return err
		}
		if err := registerKV(pctx, "shop", "prod", "cache", store, "vpc-1", "10.0.0.0/16", []string{"subnet-a"}); err != nil {
			return err
		}
		return registerBucket(pctx, "shop", "prod", "uploads", translateBucket(nil), "ocel-state", containerCfg.AppBoundaryARN, newSessionScope("shop", "prod", "arn"), testUploadCompleter())
	})

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

	if got := countTypes(set)["aws_lambda_function"]; got != 2 {
		t.Errorf("lambdas = %d, want one for web and the upload completer", got)
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

func TestAnEphemeralPreviewShapesNoWorkerItWouldNotRun(t *testing.T) {
	t.Parallel()

	req := shapeRequest(t)
	req.Deploy.Infra = naming.StackName{}
	set := shaped(t, nil, req)

	for _, r := range set.GetResources() {
		if r.GetType() == "aws_lambda_function" && (r.GetName() == "worker" || r.GetName() == "ledger") {
			t.Errorf("an ephemeral preview shapes worker %s, want none: a preview runs no workers", r.GetName())
		}
	}
}
