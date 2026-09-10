package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	containerImage  = "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixtureSecret   = "5e884898da28047151d0e56f8dc6292773603d0d"
	fixtureOrigin   = "ocel-containers-production-123.us-east-1.elb.amazonaws.com"
	fixtureListener = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/ocel-containers-production/abc/def"
)

func fixtureSubstrate() substrate {
	return substrate{
		VPC:           "vpc-123",
		Subnets:       []string{"subnet-a", "subnet-b"},
		Listener:      fixtureListener,
		OriginHost:    fixtureOrigin,
		TaskSecurity:  "sg-tasks",
		Cluster:       "arn:aws:ecs:us-east-1:123456789012:cluster/ocel-containers-production",
		ExecutionRole: "arn:aws:iam::123456789012:role/ocel-containers-production-exec-abc",
		LogGroup:      "/ocel/containers/production",
	}
}

func plannedContainerStack(t *testing.T) (Config, providerkit.StackPlan) {
	t.Helper()
	cfg := Config{
		Region:         "us-east-1",
		AppBoundaryARN: "arn:aws:iam::123456789012:policy/ocel-app-boundary",
		OriginSecret:   fixtureSecret,
	}
	stack := naming.AppStack("prod", "web", fixedRelease(t))
	plan := providerkit.StackPlan{
		Ref:    providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: stack},
		Kind:   providerkit.StackApp,
		Tags:   map[string]string{"ocel:managed-by": "ocel"},
		Images: providerkit.ImagePlan{Pushes: []providerkit.ImagePush{{App: "web", Target: containerImage}}},
		App: &providerkit.AppPlan{
			App:             "web",
			Deployment:      "d1",
			Compute:         providerkit.ComputeContainer,
			Image:           containerImage,
			HealthCheckPath: "/healthz",
			Values: providerkit.AppValues{
				Delivered: map[string]string{"GREETING": "hello", "DATABASE_URL": "postgres://db"},
				Phase:     "production",
			},
		},
	}
	return cfg, plan
}

func TestAServerlessAppThatPushesAnImageIsRefused(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedContainerStack(t)
	plan.App.Compute = providerkit.ComputeServerless
	if _, _, err := releasing(t, cfg).prepare(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "serverless") {
		t.Fatalf("prepare() of a serverless app that pushes an image = %v, want it refused: functions run no image", err)
	}
}

func TestAContainerIsHandedItsValuesAndThePortItListensOn(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedContainerStack(t)
	work, err := releasing(t, cfg).containerWork(plan, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if work.env["GREETING"] != "hello" || work.env["DATABASE_URL"] != "postgres://db" || work.env[constants.PhaseEnvName] != "production" {
		t.Errorf("env = %v, want every delivered value and the phase: a container reads its values off the environment alone", work.env)
	}
	if work.env[containerPortEnv] != containerPort {
		t.Errorf("%s = %q, want %s, the port the target group probes", containerPortEnv, work.env[containerPortEnv], containerPort)
	}

	_, err = containerEnv("web", providerkit.AppValues{Delivered: map[string]string{containerPortEnv: "3000"}})
	if err == nil || !strings.Contains(err.Error(), containerPortEnv) {
		t.Errorf("containerEnv with %s = %v, want it refused by name: the load balancer would probe a port nothing listens on", containerPortEnv, err)
	}

	plan.App.HealthCheckPath = "not a path"
	if _, err := releasing(t, cfg).containerWork(plan, fixtureSubstrate()); err == nil {
		t.Error("containerWork accepted a health check path a load balancer cannot probe")
	}
	cfg.OriginSecret = ""
	plan.App.HealthCheckPath = "/healthz"
	if _, err := releasing(t, cfg).containerWork(plan, fixtureSubstrate()); err == nil {
		t.Error("containerWork accepted a class with no origin secret, so the listener rule would admit every stranger")
	}
}

func TestAContainerStackStandsUpAFargateServiceBehindTheSharedFront(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedContainerStack(t)
	plan.App.Grants = []providerkit.Binding{{
		Type: providerkit.BindingBucket,
		Name: "bucket--uploads",
		Grants: []providerkit.Grant{{
			Label:     "objects",
			Actions:   []string{"s3:GetObject"},
			Resources: []string{"arn:aws:s3:::uploads/*"},
		}},
	}}
	release := releasing(t, cfg)
	work, err := release.containerWork(plan, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	if err := release.placeRule(context.Background(), work); err != nil {
		t.Fatalf("placeRule() = %v", err)
	}

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", plan.Ref.Name.String(), rec)); err != nil {
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
	if !task["containerDefinitions"].IsSecret() {
		t.Fatal("containerDefinitions is not a secret, so every delivered value would sit in the state checkpoint in the clear")
	}
	var definitions []containerDefinition
	if err := json.Unmarshal([]byte(task["containerDefinitions"].SecretValue().Element.StringValue()), &definitions); err != nil || len(definitions) != 1 {
		t.Fatalf("containerDefinitions = %v, want one container: %v", task["containerDefinitions"], err)
	}
	held := definitions[0]
	if held.Image != containerImage || held.Name != containerName || held.PortMappings[0].ContainerPort != containerPortNumber {
		t.Errorf("container = %+v, want the pushed image listening on %d", held, containerPortNumber)
	}
	env := map[string]string{}
	for _, pair := range held.Environment {
		env[pair.Name] = pair.Value
	}
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

	cfg, plan := plannedContainerStack(t)
	work, err := releasing(t, cfg).containerWork(plan, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", plan.Ref.Name.String(), rec)); err != nil {
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

	cfg, plan := plannedContainerStack(t)
	release := releasing(t, cfg)
	work, err := release.containerWork(plan, fixtureSubstrate())
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	plan.Options = work
	outputs := auto.OutputMap{"web": auto.OutputValue{Value: map[string]any{
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: "shop-prod-web-container-r3f8a1c90",
	}}}
	result, err := release.Decode(context.Background(), plan, outputs)
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

	if _, err := release.Decode(context.Background(), plan, auto.OutputMap{}); err == nil {
		t.Error("Decode() of a stack that produced no output succeeded, so a deploy would record a container with no origin")
	}
}

type fakeRules struct {
	taken []string
}

func (f fakeRules) DescribeRules(_ context.Context, in *elbv2.DescribeRulesInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeRulesOutput, error) {
	if aws.ToString(in.ListenerArn) != fixtureListener {
		return nil, errors.New("asked about a listener that is not the front's")
	}
	out := &elbv2.DescribeRulesOutput{}
	for _, priority := range f.taken {
		out.Rules = append(out.Rules, elbv2types.Rule{Priority: aws.String(priority)})
	}
	return out, nil
}

func TestARuleStepsPastThePrioritiesTheFrontAlreadyHolds(t *testing.T) {
	t.Parallel()

	hashed := rulePriority("shop-prod-web-container-r3f8a1c90", nil)
	cfg, plan := plannedContainerStack(t)
	cfg.Rules = fakeRules{taken: []string{strconv.Itoa(hashed), strconv.Itoa(hashed + 1), "default"}}
	release := releasing(t, cfg)
	work, err := release.containerWork(plan, fixtureSubstrate())
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
