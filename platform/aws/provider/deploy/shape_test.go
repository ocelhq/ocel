package deploy

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/pkg/naming"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/transformkit"
	"github.com/ocelhq/ocel/pkg/transformkit/transformtest"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
}

func shapeRequest(t *testing.T) providerkit.ShapeRequest {
	t.Helper()
	release := fixedRelease(t)
	return providerkit.ShapeRequest{
		Plan: providerkit.DeployPlan{
			Slug:  "shop",
			Class: edge.ClassProduction,
			Env:   "prod",
			Infra: naming.InfraStack("prod"),
			Apps: []providerkit.AppEntry{
				{App: "web", Stack: naming.AppStack("prod", "web", release), Manifest: &contractv1.ManifestApp{Name: "web", Framework: &contractv1.Framework{Name: "next"}, Compute: "serverless"}},
				{App: "api", Stack: naming.AppStack("prod", "api", release), Manifest: &contractv1.ManifestApp{Name: "api", Framework: &contractv1.Framework{Name: "go"}, Compute: "container"}},
			},
		},
		Resources: []providerkit.Resource{
			{Name: "main", Type: providerkit.BindingPostgres, Postgres: &providerkit.PostgresSpec{}},
			{Name: "uploads", Type: providerkit.BindingBucket, Bucket: &providerkit.BucketSpec{}},
			{Name: "shared", Type: providerkit.BindingBucket, Binding: "elsewhere"},
		},
		Functions: map[string][]providerkit.FunctionSpec{
			"web": {{Name: "fn--web--entry"}, {Name: "fn--web--admin", Route: "/admin"}},
		},
	}
}

func shaped(t *testing.T, pass transformkit.Pass, req providerkit.ShapeRequest) *costv1.ResourceSet {
	t.Helper()
	tree := &costkit.Tree{}
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
	cfg, appPlan := plannedAppStack(t)
	appWork, err := releasing(t, cfg).appWork(appPlan, nil)
	if err != nil {
		t.Fatal(err)
	}
	run("app", func(pctx *pulumi.Context) error { return appWork.run(pctx, nil) })

	containerCfg, containerPlan := plannedContainerStack(t)
	containerWork, err := releasing(t, containerCfg).containerWork(containerPlan, fixtureSubstrate())
	if err != nil {
		t.Fatal(err)
	}
	run("container", containerWork.run)
	run("substrate", (&substrateWork{class: edge.ClassProduction, boundary: containerCfg.AppBoundaryARN}).run)
	run(naming.InfraApp, func(pctx *pulumi.Context) error {
		if err := registerPostgres(pctx, "shop", "prod", "main", translatePostgres(nil), "vpc-1", "10.0.0.0/16", []string{"subnet-a"}); err != nil {
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
}

func TestShapeScopesAppsUnderTheEnvironmentAndTheSubstrateAsShared(t *testing.T) {
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

func TestAnAppWithNoBuildShapesOneFunction(t *testing.T) {
	t.Parallel()

	req := shapeRequest(t)
	req.Functions = nil
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
	for _, r := range set.GetResources() {
		if r.GetType() == "aws_lambda_function" && r.GetName() == "fn--web--entry" {
			if !slices.Contains(r.GetUnknown(), "vpc_config") {
				t.Errorf("unknown = %v, want vpc_config, which a binding fills at deploy", r.GetUnknown())
			}
		}
	}
}
