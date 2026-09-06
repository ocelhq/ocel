package deploy

import (
	"context"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

const containerImage = "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func plannedContainerStack(t *testing.T) (Config, providerkit.StackPlan) {
	t.Helper()
	cfg := Config{AppBoundaryARN: "arn:aws:iam::123456789012:policy/ocel-app-boundary"}
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

func TestAContainerPlanIsPreparedAsContainerWorkAndAServerlessPushIsRefused(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedContainerStack(t)
	release := releasing(t, cfg)
	prepared, app, err := release.prepare(context.Background(), plan)
	if err != nil {
		t.Fatalf("prepare() = %v", err)
	}
	if app != nil {
		t.Errorf("prepare() planned function work %+v for a container app, which has no functions to stand up", app)
	}
	work, held := prepared.Options.(*containerWork)
	if !held {
		t.Fatalf("prepare() planned %T, want the container work App Runner runs", prepared.Options)
	}
	if work.image != containerImage || work.healthPath != "/healthz" {
		t.Errorf("work = image %q at %q, want the image and health path the plan carried", work.image, work.healthPath)
	}
	if work.env["GREETING"] != "hello" || work.env["DATABASE_URL"] != "postgres://db" || work.env[providerkit.PhaseEnvName] != "production" {
		t.Errorf("env = %v, want every delivered value and the phase: a container reads its values off the environment alone", work.env)
	}

	plan.App.Compute = providerkit.ComputeServerless
	if _, _, err := release.prepare(context.Background(), plan); err == nil || !strings.Contains(err.Error(), "serverless") {
		t.Fatalf("prepare() of a serverless app that pushes an image = %v, want it refused: functions run no image", err)
	}
}

func TestAContainerRefusesValuesAppRunnerOwns(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"PORT", "AWSAPPRUNNER_REGION"} {
		_, err := containerEnv("web", providerkit.AppValues{Delivered: map[string]string{key: "x"}})
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("containerEnv with %s = %v, want it refused by name: App Runner would silently take the value's place", key, err)
		}
	}
}

func TestAContainerStackStandsUpAnAppRunnerServiceOverThePushedImage(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedContainerStack(t)
	plan.App.Grants = []providerkit.Link{{
		Type: providerkit.LinkBucket,
		Name: "bucket--uploads",
		Grants: []providerkit.Grant{{
			Label:     "objects",
			Actions:   []string{"s3:GetObject"},
			Resources: []string{"arn:aws:s3:::uploads/*"},
		}},
	}}
	release := releasing(t, cfg)
	work, err := release.containerWork(plan)
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}

	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", plan.Ref.Name.String(), rec)); err != nil {
		t.Fatalf("run the container program: %v", err)
	}

	service := recordedOf(t, rec, "aws:apprunner/service:Service")
	if name := service["serviceName"].StringValue(); name != "shop-prod-web-container-r3f8a1c90" || len(name) > maxServiceNameLen {
		t.Errorf("serviceName = %q, want project, env, app and release within App Runner's %d characters", name, maxServiceNameLen)
	}
	source := service["sourceConfiguration"].ObjectValue()
	repo := source["imageRepository"].ObjectValue()
	if repo["imageIdentifier"].StringValue() != containerImage || repo["imageRepositoryType"].StringValue() != "ECR" {
		t.Errorf("image repository = %v, want the pushed ECR coordinate", repo)
	}
	image := repo["imageConfiguration"].ObjectValue()
	if image["port"].StringValue() != containerPort {
		t.Errorf("port = %v, want %s, the port every ocel container listens on", image["port"], containerPort)
	}
	env := image["runtimeEnvironmentVariables"].ObjectValue()
	if env["GREETING"].StringValue() != "hello" || env[providerkit.PhaseEnvName].StringValue() != "production" {
		t.Errorf("runtime env = %v, want the delivered values and the phase", env)
	}
	if source["autoDeploymentsEnabled"].BoolValue() {
		t.Error("auto deployments are on, so a repointed tag would roll the service under a release that never asked for it")
	}
	health := service["healthCheckConfiguration"].ObjectValue()
	if health["protocol"].StringValue() != "HTTP" || health["path"].StringValue() != "/healthz" {
		t.Errorf("health check = %v, want an HTTP probe of the path the manifest named", health)
	}
	if service["instanceConfiguration"].ObjectValue()["instanceRoleArn"].StringValue() == "" {
		t.Error("an app granted a link runs without an instance role, so the grant reaches nothing")
	}

	trusts := map[string]string{}
	for key, inputs := range rec.recorded {
		if strings.HasPrefix(key, "aws:iam/role:Role::") {
			trusts[strings.TrimPrefix(key, "aws:iam/role:Role::")] = inputs["assumeRolePolicy"].StringValue()
		}
	}
	if !strings.Contains(trusts["role-pull"], appRunnerBuildPrincipal) {
		t.Errorf("the pull role trusts %q, want %s, which is what pulls the image", trusts["role-pull"], appRunnerBuildPrincipal)
	}
	if !strings.Contains(trusts["role-app"], appRunnerTasksPrincipal) {
		t.Errorf("the instance role trusts %q, want %s, which is what the container runs as", trusts["role-app"], appRunnerTasksPrincipal)
	}
	if len(rec.attached) != 1 || rec.attached[0] != appRunnerECRAccessPolicyARN {
		t.Errorf("attached %v, want only the ECR access policy App Runner pulls with", rec.attached)
	}
}

func TestAContainerWithNoGrantsRunsWithoutAnInstanceRole(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedContainerStack(t)
	work, err := releasing(t, cfg).containerWork(plan)
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(func(pctx *pulumi.Context) error { return work.run(pctx) }, pulumi.WithMocks("shop", plan.Ref.Name.String(), rec)); err != nil {
		t.Fatalf("run the container program: %v", err)
	}
	if _, minted := rec.recorded["aws:iam/role:Role::role-app"]; minted {
		t.Error("an app granted nothing was minted an instance role, which can only widen what the container reaches")
	}
}

func TestAContainerStackDecodesIntoTheContainerItStoodUp(t *testing.T) {
	t.Parallel()

	cfg, plan := plannedContainerStack(t)
	release := releasing(t, cfg)
	prepared, _, err := release.prepare(context.Background(), plan)
	if err != nil {
		t.Fatalf("prepare() = %v", err)
	}
	outputs := auto.OutputMap{"web": auto.OutputValue{Value: map[string]any{
		outputKeyContainerURL: "https://abc.us-east-1.awsapprunner.com",
		outputKeyContainerARN: "arn:aws:apprunner:us-east-1:123456789012:service/shop-prod-web-container-r3f8a1c90/abc",
	}}}
	result, err := release.Decode(context.Background(), prepared, outputs)
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
	if held.Name != "web" || held.URL != "https://abc.us-east-1.awsapprunner.com" || held.Image != containerImage || !strings.HasPrefix(held.Physical, "arn:aws:apprunner:") {
		t.Errorf("container = %+v, want the app's name, the service URL an edge reaches, the image it runs and the service ARN", held)
	}

	if _, err := release.Decode(context.Background(), prepared, auto.OutputMap{}); err == nil {
		t.Error("Decode() of a stack that produced no output succeeded, so a deploy would record a container with no URL")
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
