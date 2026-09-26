package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/runtimekit/originguard"
	"github.com/ocelhq/ocel/pkg/transformkit"
	vars "github.com/ocelhq/ocel/platform/aws/provider/vars/live"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	containerImage  = "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixtureSecret   = "5e884898da28047151d0e56f8dc6292773603d0d"
	fixtureOrigin   = "internal-ocel-containers-production-123.us-east-1.elb.amazonaws.com"
	fixtureFront    = "vo_2XyZ3abc4DEF5ghi"
	fixtureListener = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/ocel-containers-production/abc/def"
)

func fixtureSubstrate() substrate {
	return substrate{
		VPC:           "vpc-123",
		Subnets:       []string{"subnet-a", "subnet-b"},
		Listener:      fixtureListener,
		OriginHost:    fixtureOrigin,
		VPCOrigin:     fixtureFront,
		TaskSecurity:  "sg-tasks",
		Cluster:       "arn:aws:ecs:us-east-1:123456789012:cluster/ocel-containers-production",
		ExecutionRole: "arn:aws:iam::123456789012:role/ocel-containers-production-exec-abc",
		LogGroup:      "/ocel/containers/production",
	}
}

func containerStackSpec(t *testing.T) (Config, provider.StackSpec) {
	t.Helper()
	cfg := Config{
		Region:         "us-east-1",
		AppBoundaryARN: "arn:aws:iam::123456789012:policy/ocel-app-boundary",
		OriginSecret:   fixtureSecret,
		Slug:           "shop",
		Class:          edge.ClassProduction,
		VarsTable:      "ocel-vars",
		VarsTableARN:   "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-vars",
		VarsKeyARN:     "arn:aws:kms:us-east-1:123456789012:key/abcd",
	}
	stack := naming.AppStack("prod", "web", fixedRelease(t))
	spec := provider.StackSpec{
		Ref:    provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: stack},
		Kind:   provider.StackApp,
		Tags:   map[string]string{"ocel:managed-by": "ocel"},
		Images: provider.ImagePushes{Pushes: []images.Push{{App: "web", ImageRef: containerImage}}},
		App: &provider.AppSpec{
			App:             "web",
			Deployment:      "d1",
			Compute:         provider.ComputeContainer,
			Image:           containerImage,
			HealthCheckPath: "/healthz",
			Values: provider.AppValues{
				Plain:     map[string]string{"GREETING": "hello"},
				Sensitive: map[string]string{"API_TOKEN": "sensitive-token"},
				Phase:     "production",
			},
		},
	}
	return cfg, spec
}

func declaringASecret(spec provider.StackSpec) provider.StackSpec {
	spec.App.Values.Secrets = []provider.SecretRef{{Key: "DATABASE_URL"}}
	return spec
}

func definitionEnvOf(t *testing.T, rec *inputRecorder) map[string]string {
	t.Helper()
	task := recordedOf(t, rec, "aws:ecs/taskDefinition:TaskDefinition")
	if !task["containerDefinitions"].IsSecret() {
		t.Fatal("containerDefinitions is not a secret, so every delivered value would sit in the state checkpoint in the clear")
	}
	var definitions []containerDefinition
	if err := json.Unmarshal([]byte(task["containerDefinitions"].SecretValue().Element.StringValue()), &definitions); err != nil || len(definitions) != 1 {
		t.Fatalf("containerDefinitions = %v, want one container: %v", task["containerDefinitions"], err)
	}
	env := map[string]string{}
	for _, pair := range definitions[0].Environment {
		env[pair.Name] = pair.Value
	}
	return env
}

func TestAServerlessAppThatPushesAnImageIsRefused(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	spec.App.Compute = provider.ComputeServerless
	if _, _, err := releasing(t, cfg).prepare(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "serverless") {
		t.Fatalf("prepare() of a serverless app that pushes an image = %v, want it refused: functions run no image", err)
	}
}

func TestAContainerIsHandedItsValuesAndThePortItListensOn(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	work, err := releasing(t, cfg).containerWork(spec, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if work.env["GREETING"] != "hello" || work.env["API_TOKEN"] != "sensitive-token" || work.env[constants.PhaseEnvName] != "production" {
		t.Errorf("env = %v, want the plain and sensitive values and the phase: those are handed to the runtime once, in the task definition", work.env)
	}
	if work.env[containerPortEnv] != containerPort {
		t.Errorf("%s = %q, want %s, the port the target group probes", containerPortEnv, work.env[containerPortEnv], containerPort)
	}
	if work.env[edge.OriginSecretVar] != fixtureSecret {
		t.Errorf("%s = %q, want the class's origin secret: the runtime in the container guards with it the way the node runtime does on Lambda", edge.OriginSecretVar, work.env[edge.OriginSecretVar])
	}
	if _, pinned := work.env[vars.EnvVar]; pinned {
		t.Errorf("env carries %s for an app that declares no secret and no binding, so the runtime would open a store it has nothing to read from", vars.EnvVar)
	}
	if work.readsLive() {
		t.Error("an app with nothing live is granted a read on the variable table")
	}
	if got := work.definitionEnv()[originguard.HealthPathVar]; got != "/healthz" {
		t.Errorf("%s = %q, want the manifest's probe path, which the runtime answers without the origin secret", originguard.HealthPathVar, got)
	}

	_, err = containerEnv("web", provider.AppValues{Plain: map[string]string{containerPortEnv: "3000"}}, fixtureSecret, "", nil)
	if err == nil || !strings.Contains(err.Error(), containerPortEnv) {
		t.Errorf("containerEnv with %s = %v, want it refused by name: the load balancer would probe a port nothing listens on", containerPortEnv, err)
	}

	spec.App.HealthCheckPath = "not a path"
	if _, err := releasing(t, cfg).containerWork(spec, fixtureSubstrate()); err == nil {
		t.Error("containerWork accepted a health check path a load balancer cannot probe")
	}
	cfg.OriginSecret = ""
	spec.App.HealthCheckPath = "/healthz"
	if _, err := releasing(t, cfg).containerWork(spec, fixtureSubstrate()); err == nil {
		t.Error("containerWork accepted a class with no origin secret, so the listener rule would admit every stranger")
	}
}

func TestAContainerDeployedDuringARotationAcceptsBothSecretsAndItsRuleAdmitsEither(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	cfg.PreviousOriginSecret = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	release := releasing(t, cfg)
	work, err := release.containerWork(spec, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if work.env[edge.OriginSecretVar] != fixtureSecret || work.env[edge.OriginSecretPreviousVar] != cfg.PreviousOriginSecret {
		t.Errorf("env carries %q and %q, want the current secret and the one it replaced: a route written before the rotation still presents the old one", work.env[edge.OriginSecretVar], work.env[edge.OriginSecretPreviousVar])
	}
	if err := release.placeRule(context.Background(), work); err != nil {
		t.Fatalf("placeRule() = %v", err)
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", spec.Ref.Name.String(), rec)); err != nil {
		t.Fatalf("run the container program: %v", err)
	}
	rule := recordedOf(t, rec, "aws:lb/listenerRule:ListenerRule")
	for _, condition := range rule["conditions"].ArrayValue() {
		header := condition.ObjectValue()["httpHeader"].ObjectValue()
		if header["httpHeaderName"].StringValue() != edge.OriginSecretHeader {
			continue
		}
		var admitted []string
		for _, value := range header["values"].ArrayValue() {
			admitted = append(admitted, value.StringValue())
		}
		if !slices.Equal(admitted, []string{fixtureSecret, cfg.PreviousOriginSecret}) {
			t.Errorf("the rule admits %v, want the current secret and the one it replaced", admitted)
		}
	}

	cfg.PreviousOriginSecret = ""
	settled, err := releasing(t, cfg).containerWork(spec, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if _, held := settled.env[edge.OriginSecretPreviousVar]; held || len(settled.secrets) != 1 {
		t.Errorf("a class with no rotation underway hands the container %v and env %v, want the current secret alone", settled.secrets, settled.env)
	}
}

func TestAContainerStackStandsUpAFargateServiceBehindTheSharedFront(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	spec.App.Grants = []provider.Binding{{
		Type: provider.BindingBucket,
		Name: "bucket--uploads",
		Grants: []provider.Grant{{
			Label:     "objects",
			Actions:   []string{"s3:GetObject"},
			Resources: []string{"arn:aws:s3:::uploads/*"},
		}},
	}}
	release := releasing(t, cfg)
	work, err := release.containerWork(spec, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if err := release.placeRule(context.Background(), work); err != nil {
		t.Fatalf("placeRule() = %v", err)
	}

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", spec.Ref.Name.String(), rec)); err != nil {
		t.Fatalf("run the container program: %v", err)
	}

	task := recordedOf(t, rec, "aws:ecs/taskDefinition:TaskDefinition")
	if task["family"].StringValue() != "shop-prod-web-container-r3f8a1c90" {
		t.Errorf("family = %v, want project, env, app and release", task["family"])
	}
	if task["executionRoleArn"].StringValue() != fixtureSubstrate().ExecutionRole {
		t.Errorf("executionRoleArn = %v, want the substrate's shared execution role", task["executionRoleArn"])
	}
	if task["taskRoleArn"].StringValue() == "" {
		t.Error("an app granted a binding runs without a task role, so the grant reaches nothing")
	}
	var definitions []containerDefinition
	if err := json.Unmarshal([]byte(task["containerDefinitions"].SecretValue().Element.StringValue()), &definitions); err != nil || len(definitions) != 1 {
		t.Fatalf("containerDefinitions = %v, want one container: %v", task["containerDefinitions"], err)
	}
	held := definitions[0]
	if held.Image != containerImage || held.Name != containerName || held.PortMappings[0].ContainerPort != containerPortNumber {
		t.Errorf("container = %+v, want the pushed image listening on %d", held, containerPortNumber)
	}
	env := definitionEnvOf(t, rec)
	if env["GREETING"] != "hello" || env[containerPortEnv] != containerPort {
		t.Errorf("environment = %v, want the delivered values and the port", env)
	}
	if held.LogConfig.Options["awslogs-group"] != "/ocel/containers/production" || held.LogConfig.Options["awslogs-region"] != "us-east-1" {
		t.Errorf("logs = %v, want the class log group in the deploy's region", held.LogConfig)
	}

	group := recordedOf(t, rec, "aws:lb/targetGroup:TargetGroup")
	health := group["healthCheck"].ObjectValue()
	if health["path"].StringValue() != "/healthz" || group["targetType"].StringValue() != "ip" || group["vpcId"].StringValue() != "vpc-123" {
		t.Errorf("target group = %v, want an ip target group in the substrate's VPC probed on the manifest's path", group)
	}

	rule := recordedOf(t, rec, "aws:lb/listenerRule:ListenerRule")
	if rule["listenerArn"].StringValue() != fixtureListener {
		t.Errorf("listenerArn = %v, want the substrate's listener", rule["listenerArn"])
	}
	if priority := rule["priority"].NumberValue(); priority < 1 || priority > maxRulePriority || priority != float64(rulePriority("shop-prod-web-container-r3f8a1c90", nil)) {
		t.Errorf("priority = %v, want one derived from the service name: two releases stood up at once must not both take the next free slot", priority)
	}
	demanded := map[string]string{}
	for _, condition := range rule["conditions"].ArrayValue() {
		header := condition.ObjectValue()["httpHeader"].ObjectValue()
		demanded[header["httpHeaderName"].StringValue()] = header["values"].ArrayValue()[0].StringValue()
	}
	if demanded[edge.OriginSecretHeader] != fixtureSecret {
		t.Errorf("the rule demands %v, want the class's origin secret: without it every stranger reaches the container", demanded)
	}
	if demanded[edge.OriginContainerHeader] != "shop-prod-web-container-r3f8a1c90" {
		t.Errorf("the rule demands %v, want the container the edge names: every release shares one front", demanded)
	}

	service := recordedOf(t, rec, "aws:ecs/service:Service")
	if service["launchType"].StringValue() != "FARGATE" || service["cluster"].StringValue() != fixtureSubstrate().Cluster {
		t.Errorf("service = %v, want a Fargate service in the substrate's cluster", service)
	}
	network := service["networkConfiguration"].ObjectValue()
	if network["securityGroups"].ArrayValue()[0].StringValue() != "sg-tasks" || len(network["subnets"].ArrayValue()) != 2 {
		t.Errorf("network = %v, want the substrate's task security group and subnets", network)
	}
	if !service["waitForSteadyState"].BoolValue() {
		t.Error("the deploy does not wait for the service to settle, so it would record a container nothing answers on yet")
	}

	for key, inputs := range rec.recorded {
		if strings.HasPrefix(key, "aws:iam/role:Role::") && !strings.Contains(inputs["assumeRolePolicy"].StringValue(), ecsTasksPrincipal) {
			t.Errorf("%s trusts %q, want %s, which is what the task runs as", key, inputs["assumeRolePolicy"].StringValue(), ecsTasksPrincipal)
		}
	}
}

func TestAContainerWithNoGrantsRunsWithoutATaskRole(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	work, err := releasing(t, cfg).containerWork(spec, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", spec.Ref.Name.String(), rec)); err != nil {
		t.Fatalf("run the container program: %v", err)
	}
	for key := range rec.recorded {
		if strings.HasPrefix(key, "aws:iam/role:Role::") {
			t.Errorf("an app granted nothing was minted %s, which can only widen what the container reaches", key)
		}
	}
}

func TestAContainerStackDecodesIntoTheContainerItStoodUp(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	release := releasing(t, cfg)
	work, err := release.containerWork(spec, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	spec.VendorState = work
	outputs := auto.OutputMap{"web": auto.OutputValue{Value: map[string]any{
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: "shop-prod-web-container-r3f8a1c90",
	}}}
	result, err := release.Decode(context.Background(), spec, outputs)
	if err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if len(result.Functions) != 0 {
		t.Errorf("Functions = %v, want none: a container stack stands up no function", result.Functions)
	}
	if len(result.Containers) != 1 {
		t.Fatalf("Containers = %v, want the one service the stack stood up", result.Containers)
	}
	held := result.Containers[0]
	if held.Name != "web" || held.URL != "http://"+fixtureOrigin || held.Image != containerImage || held.Physical != "shop-prod-web-container-r3f8a1c90" {
		t.Errorf("container = %+v, want the app's name, the front the edge reaches, the image it runs and the service the rule names", held)
	}

	if _, err := release.Decode(context.Background(), spec, auto.OutputMap{}); err == nil {
		t.Error("Decode() of a stack that produced no output succeeded, so a deploy would record a container with no origin")
	}
}

type fakeRules struct {
	mu    sync.Mutex
	taken []string
	held  []elbv2types.Rule
}

func (f *fakeRules) DescribeRules(_ context.Context, in *elbv2.DescribeRulesInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeRulesOutput, error) {
	if aws.ToString(in.ListenerArn) != fixtureListener {
		return nil, errors.New("asked about a listener that is not the front's")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &elbv2.DescribeRulesOutput{Rules: slices.Clone(f.held)}
	for _, priority := range f.taken {
		out.Rules = append(out.Rules, elbv2types.Rule{Priority: aws.String(priority)})
	}
	return out, nil
}

func (f *fakeRules) claim(priority int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.taken = append(f.taken, strconv.Itoa(priority))
}

func ruleRouting(priority int, physical string) elbv2types.Rule {
	return elbv2types.Rule{
		Priority: aws.String(strconv.Itoa(priority)),
		Conditions: []elbv2types.RuleCondition{
			{Field: aws.String("http-header"), HttpHeaderConfig: &elbv2types.HttpHeaderConditionConfig{
				HttpHeaderName: aws.String(edge.OriginSecretHeader), Values: []string{"secret"},
			}},
			{Field: aws.String("http-header"), HttpHeaderConfig: &elbv2types.HttpHeaderConditionConfig{
				HttpHeaderName: aws.String(edge.OriginContainerHeader), Values: []string{physical},
			}},
		},
	}
}

func TestARuleStepsPastThePrioritiesTheFrontAlreadyHolds(t *testing.T) {
	t.Parallel()

	hashed := rulePriority("shop-prod-web-container-r3f8a1c90", nil)
	cfg, spec := containerStackSpec(t)
	cfg.Rules = &fakeRules{taken: []string{strconv.Itoa(hashed), strconv.Itoa(hashed + 1), "default"}}
	release := releasing(t, cfg)
	work, err := release.containerWork(spec, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if err := release.placeRule(context.Background(), work); err != nil {
		t.Fatalf("placeRule() = %v", err)
	}
	if work.priority != hashed+2 {
		t.Errorf("priority = %d, want %d: the two slots the hash landed on are taken, and a collision is a refused deploy", work.priority, hashed+2)
	}
}

func recordedOf(t *testing.T, rec *inputRecorder, typeToken string) resource.PropertyMap {
	t.Helper()
	for key, inputs := range rec.recorded {
		if strings.HasPrefix(key, typeToken+"::") {
			return inputs
		}
	}
	t.Fatalf("the program declared no %s among %d resources", typeToken, len(rec.recorded))
	return nil
}

func TestAContainerDeclaringASecretIsHandedAManifestAndAFencedReadRatherThanThePlaintext(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	spec = declaringASecret(spec)
	release := releasing(t, cfg)
	work, err := release.containerWork(spec, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	manifest, err := vars.Parse([]byte(work.env[vars.EnvVar]))
	if err != nil {
		t.Fatalf("%s = %q, which the runtime cannot read: %v", vars.EnvVar, work.env[vars.EnvVar], err)
	}
	if len(manifest.Keys) != 1 || manifest.Keys[0].Key != "DATABASE_URL" || manifest.Table != cfg.VarsTable || manifest.KeyARN != cfg.VarsKeyARN || manifest.Slug != "shop" {
		t.Errorf("manifest = %+v, want the secret pinned by name with the table and key the runtime reads it through", manifest)
	}
	for name, value := range work.definitionEnv() {
		if strings.Contains(value, "postgres://") || name == "DATABASE_URL" {
			t.Errorf("the task definition carries %s=%q: a secret's plaintext is readable by anyone who can describe the task definition, so the runtime reads it live instead", name, value)
		}
	}
	if !work.readsLive() {
		t.Fatal("an app declaring a secret is granted no read on the variable table, so the runtime cannot resolve it")
	}

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", spec.Ref.Name.String(), rec)); err != nil {
		t.Fatalf("run the container program: %v", err)
	}
	if recordedOf(t, rec, "aws:ecs/taskDefinition:TaskDefinition")["taskRoleArn"].StringValue() == "" {
		t.Error("the task runs without a role, so the runtime has nothing to read the store with")
	}
	var policies []string
	for key, inputs := range rec.recorded {
		if strings.HasPrefix(key, "aws:iam/rolePolicy:RolePolicy::") {
			policies = append(policies, inputs["policy"].StringValue())
		}
	}
	if len(policies) != 1 {
		t.Fatalf("the task role carries %d policies, want the one vars read policy Lambda's execution role gets", len(policies))
	}
	own, _ := valuePartition("shop", string(edge.ClassProduction))
	for _, want := range []string{"kms:Decrypt", cfg.VarsKeyARN, "dynamodb:Query", cfg.VarsTableARN, own} {
		if !strings.Contains(policies[0], want) {
			t.Errorf("policy = %s, want it to carry %q: the read is fenced to this project's partition and the class key", policies[0], want)
		}
	}
}

func TestATransformTagsAContainersResourcesAndAPatchNothingCarriesIsRefused(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	pass := &fakePass{tags: map[string]string{"team": "shop"}}
	cfg.Transform = pass
	release := releasing(t, cfg)
	work, err := release.containerWork(spec, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if work.transformed, err = transformStackSpec(context.Background(), cfg.Transform, spec); err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	if len(pass.seen.Resources) != 1 || pass.seen.Resources[0].Type != transformTypeContainer || pass.seen.Resources[0].App != "web" {
		t.Fatalf("the transform was shown %+v, want the container as the one resource of app web", pass.seen.Resources)
	}
	if err := release.placeRule(context.Background(), work); err != nil {
		t.Fatalf("placeRule() = %v", err)
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", spec.Ref.Name.String(), rec)); err != nil {
		t.Fatalf("run the container program: %v", err)
	}
	for _, token := range []string{"aws:ecs/service:Service", "aws:ecs/taskDefinition:TaskDefinition", "aws:lb/targetGroup:TargetGroup"} {
		if got := recordedOf(t, rec, token)["tags"].ObjectValue()["team"]; got.StringValue() != "shop" {
			t.Errorf("%s tags carry team=%v, want the transform's tag: a transform reaching a container app was silently dropped before", token, got)
		}
	}

	pass.out = []transformkit.Patches{{"role": {"description": "patched"}}}
	if work.transformed, err = transformStackSpec(context.Background(), cfg.Transform, spec); err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", spec.Ref.Name.String(), &inputRecorder{})); err != nil {
		t.Fatalf("run the container program: %v", err)
	}
	if err := work.transformed.refuseUnclaimed(); err == nil || !strings.Contains(err.Error(), "role") {
		t.Errorf("refuseUnclaimed() = %v, want the patch on a role this app never minted refused by name rather than dropped", err)
	}
}

func TestAContainerWhoseClassResolvedNoAppBoundaryIsRefusedBeforeARoleIsMinted(t *testing.T) {
	cfg, spec := containerStackSpec(t)
	cfg.AppBoundaryARN = ""
	_, err := releasing(t, cfg).containerWork(spec, fixtureSubstrate())
	if err == nil || !strings.Contains(err.Error(), "boundary") {
		t.Fatalf("containerWork() with no boundary = %v, want a refusal naming the boundary", err)
	}
}

func TestAContainersTaskIsStoodUpOnTheArchitectureItsAppDeclares(t *testing.T) {
	t.Parallel()

	for declared, want := range map[string]string{"": "X86_64", arch.X8664: "X86_64", arch.ARM64: "ARM64"} {
		cfg, spec := containerStackSpec(t)
		spec.App.Arch = declared
		release := releasing(t, cfg)
		work, err := release.containerWork(spec, fixtureSubstrate())
		if err != nil {
			t.Fatalf("containerWork() = %v", err)
		}
		if err := release.placeRule(context.Background(), work); err != nil {
			t.Fatalf("placeRule() = %v", err)
		}
		rec := &inputRecorder{}
		if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", spec.Ref.Name.String(), rec)); err != nil {
			t.Fatalf("run the container program: %v", err)
		}
		task := recordedOf(t, rec, "aws:ecs/taskDefinition:TaskDefinition")
		if got := task["runtimePlatform"].ObjectValue()["cpuArchitecture"].StringValue(); got != want {
			t.Errorf("an app declaring arch %q runs on %s, want %s: its image is built for the architecture it declares", declared, got, want)
		}
	}
}
