package deploy

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/compute/container"
	awsresources "github.com/ocelhq/ocel/platform/aws/provider/resources"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/containers"
)

const (
	containerImage  = "123456789012.dkr.ecr.us-east-1.amazonaws.com/ocel/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixtureSecret   = "5e884898da28047151d0e56f8dc6292773603d0d"
	fixtureOrigin   = "internal-ocel-containers-production-123.us-east-1.elb.amazonaws.com"
	fixtureFront    = "vo_2XyZ3abc4DEF5ghi"
	fixtureListener = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/ocel-containers-production/abc/def"
)

func fixtureContainerInfra() containers.Infrastructure {
	return containers.Infrastructure{
		VPC:           "vpc-123",
		Subnets:       []string{"subnet-a", "subnet-b"},
		Listener:      fixtureListener,
		OriginHost:    fixtureOrigin,
		VPCOrigin:     fixtureFront,
		TaskSecurity:  "sg-tasks",
		Clients:       "sg-clients",
		Cluster:       "arn:aws:ecs:us-east-1:123456789012:cluster/ocel-containers-production",
		ExecutionRole: "arn:aws:iam::123456789012:role/ocel-containers-production-exec-abc",
		LogGroup:      "/ocel/containers/production",
	}
}

func containerStackSpec(t *testing.T) (Config, provider.StackSpec) {
	t.Helper()
	cfg := Config{
		Namespace:         testNamespace,
		Region:            "us-east-1",
		AppBoundaryARN:    "arn:aws:iam::123456789012:policy/ocel-app-boundary",
		AppRolePath:       componenttest.AppRolePath,
		OriginSecret:      fixtureSecret,
		Slug:              "shop",
		Tier:              environment.TierProduction,
		VariablesTable:    "ocel-variables",
		VariablesTableARN: "arn:aws:dynamodb:us-east-1:123456789012:table/ocel-variables",
		VariablesKeyARN:   "arn:aws:kms:us-east-1:123456789012:key/abcd",
	}
	stack := naming.AppStack("prod", "web", fixedRelease(t))
	spec := provider.StackSpec{
		Ref:      provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack},
		Kind:     provider.StackApp,
		Recorded: true,
		Tags:     map[string]string{"ocel:managed-by": "ocel"},
		Images:   provider.ImagePushes{Store: fake.NewImages(), Pushes: []provider.ImagePush{{App: "web", ImageRef: containerImage}}},
		App: &provider.AppSpec{
			App:             "web",
			BuildID:         "d1",
			Compute:         provider.ComputeContainer,
			Image:           containerImage,
			HealthCheckPath: "/healthz",
			Instances:       provider.Instances{Min: 1, Max: 1},
			Values: provider.AppValues{
				Plain:     map[string]string{"GREETING": "hello"},
				Sensitive: map[string]string{"API_TOKEN": "sensitive-token"},
				Phase:     "production",
			},
		},
	}
	return cfg, spec
}

func TestAServerlessAppThatPushesAnImageIsRefused(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	spec.App.Compute = provider.ComputeServerless
	if _, err := releasing(t, cfg).prepare(context.Background(), spec, runProvision, nil); err == nil || !strings.Contains(err.Error(), "serverless") {
		t.Fatalf("prepare() of a serverless app that pushes an image = %v, want it refused: functions run no image", err)
	}
}

type fakeRules struct {
	mu       sync.Mutex
	taken    []string
	existing []elbv2types.Rule
}

func (f *fakeRules) DescribeRules(_ context.Context, in *elbv2.DescribeRulesInput, _ ...func(*elbv2.Options)) (*elbv2.DescribeRulesOutput, error) {
	if aws.ToString(in.ListenerArn) != fixtureListener {
		return nil, errors.New("asked about a listener that is not the front's")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &elbv2.DescribeRulesOutput{Rules: slices.Clone(f.existing)}
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

func TestATransformTagsAContainersResourcesAndAPatchNothingClaimsIsRefused(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	pass := &fakePass{tags: map[string]string{"team": "shop"}}
	cfg.Transform = pass
	release := releasing(t, cfg)
	work, err := containerDefinitionOf(cfg, spec)
	if err != nil {
		t.Fatalf("containerWork() = %v", err)
	}
	transformed, err := transformStackSpec(context.Background(), testNamespace, cfg.Transform, awsresources.Registry(awsresources.Clients{}), spec)
	if err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	work.TransformTags = transformed.TagsFor(transformTypeContainer, work.App)
	if len(pass.seen.Resources) != 1 || pass.seen.Resources[0].Type != transformTypeContainer || pass.seen.Resources[0].App != "web" {
		t.Fatalf("the transform was shown %+v, want the container as the one resource of app web", pass.seen.Resources)
	}
	if err := work.PlaceRule(context.Background(), cfg.Rules); err != nil {
		t.Fatalf("placeRule() = %v", err)
	}
	rec := &inputRecorder{}
	if err := pulumi.RunErr(release.containerProgram(spec, work, transformed).Run, pulumi.WithMocks("shop", spec.Ref.Name.String(), rec)); err != nil {
		t.Fatalf("run the container program: %v", err)
	}
	for _, token := range []string{"aws:ecs/service:Service", "aws:ecs/taskDefinition:TaskDefinition", "aws:lb/targetGroup:TargetGroup"} {
		if got := recordedOf(t, rec, token)["tags"].ObjectValue()["team"]; got.StringValue() != "shop" {
			t.Errorf("%s is tagged team=%v, want the transform's tag: a transform reaching a container app was silently dropped before", token, got)
		}
	}

	pass.out = []transform.Patches{{"role": {"description": "patched"}}}
	if transformed, err = transformStackSpec(context.Background(), testNamespace, cfg.Transform, awsresources.Registry(awsresources.Clients{}), spec); err != nil {
		t.Fatalf("transformStackPlan() = %v", err)
	}
	work.TransformTags = transformed.TagsFor(transformTypeContainer, work.App)
	if err := pulumi.RunErr(release.containerProgram(spec, work, transformed).Run, pulumi.WithMocks("shop", spec.Ref.Name.String(), &inputRecorder{})); err != nil {
		t.Fatalf("run the container program: %v", err)
	}
	if err := transformed.refuseUnclaimed(); err == nil || !strings.Contains(err.Error(), "role") {
		t.Errorf("refuseUnclaimed() = %v, want the patch on a role this app never minted refused by name rather than dropped", err)
	}
}

type retaining struct {
	mu         sync.Mutex
	reconciled []string
	stores     []provider.ImageStore
	forgotten  []string
	forgetting []provider.ImageStore
	refusal    error
}

func (r *retaining) hooks() *resources.ImageRetentionHooks {
	return &resources.ImageRetentionHooks{
		Reconcile: func(_ context.Context, _ provider.StackRef, app, imageRef string, images provider.ImageStore, _ progress.Log) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.reconciled = append(r.reconciled, app+" "+imageRef)
			r.stores = append(r.stores, images)
			return r.refusal
		},
		Forget: func(_ context.Context, _ provider.StackRef, app string, images provider.ImageStore, _ progress.Log) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.forgotten = append(r.forgotten, app)
			r.forgetting = append(r.forgetting, images)
			return r.refusal
		},
	}
}

func retainingContainers(t *testing.T) (*Stacks, *mockedEngine, *retaining, provider.StackSpec) {
	t.Helper()
	cfg, spec := containerStackSpec(t)
	cfg.KeyValues = fake.NewKeyValues()
	cfg.BackendURL = "s3://ocel-state/conformance"
	cfg.PulumiProject = "ocel-conformance"
	cfg.Passphrase = "a-passphrase"
	retained := &retaining{}
	cfg.Retention = retained.hooks()
	outputs := containerInfraOutputs()
	outputs["web"] = auto.OutputValue{Value: map[string]any{
		container.OutputKeyURL:      "http://" + fixtureOrigin,
		container.OutputKeyPhysical: "shop-prod-web-container-r3f8a1c90",
	}}
	engine := &mockedEngine{outputs: outputs}
	return stacksWith(cfg, engine), engine, retained, spec
}

func TestAContainerReleaseReconcilesItsImageWithTheStoreItPushedTo(t *testing.T) {
	t.Parallel()

	stacks, _, retained, spec := retainingContainers(t)
	pushed := fake.NewImages()
	spec.Images.Store = pushed

	if _, err := stacks.Provision(context.Background(), spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	if want := []string{"web " + containerImage}; !slices.Equal(retained.reconciled, want) {
		t.Errorf("the release reconciled %v, want %v", retained.reconciled, want)
	}
	if len(retained.stores) != 1 || retained.stores[0] != provider.ImageStore(pushed) {
		t.Errorf("the reconcile was handed %v, want the store the release pushed to", retained.stores)
	}
}

func TestAContainerReleaseThatFailsStillReconcilesTheImageItPushed(t *testing.T) {
	t.Parallel()

	stacks, engine, retained, spec := retainingContainers(t)
	engine.upErr = func(string) error { return errors.New("the service never became healthy") }

	if _, err := stacks.Provision(context.Background(), spec, progress.Discard()); err == nil {
		t.Fatal("Provision() succeeded, and this case is the failure path")
	}

	if want := []string{"web " + containerImage}; !slices.Equal(retained.reconciled, want) {
		t.Errorf("the failed release reconciled %v, want %v: the image was pushed before the service failed, and nothing else will ever reference it", retained.reconciled, want)
	}
}

func TestAReconcileThatFailsDoesNotFailTheRelease(t *testing.T) {
	t.Parallel()

	stacks, _, retained, spec := retainingContainers(t)
	retained.refusal = errors.New("ecr:DescribeImages is not granted")

	if _, err := stacks.Provision(context.Background(), spec, progress.Discard()); err != nil {
		t.Errorf("Provision() = %v, want the release to land: the images it could not reclaim are a cost, not a failure", err)
	}
}

func TestADeployThatRunsNoContainerReconcilesNoImage(t *testing.T) {
	t.Parallel()

	cfg, spec := containerStackSpec(t)
	retained := &retaining{}
	cfg.Retention = retained.hooks()
	spec.App.Compute = provider.ComputeServerless
	spec.Images = provider.ImagePushes{}
	spec.App.Image = ""

	_, _ = releasing(t, cfg).prepare(context.Background(), spec, runProvision, nil)

	if len(retained.reconciled) != 0 {
		t.Errorf("a serverless release reconciled %v, want no image", retained.reconciled)
	}
}

func TestDestroyingAContainerStackForgetsItsAppThroughTheStoreItWasHanded(t *testing.T) {
	t.Parallel()

	stacks, _, retained, spec := retainingContainers(t)
	ctx := context.Background()
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	pushed := fake.NewImages()

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: spec.Ref, Images: pushed}, progress.Discard()); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}

	if want := []string{"web"}; !slices.Equal(retained.forgotten, want) {
		t.Errorf("the destroy forgot %v, want %v: the images the stack ran are reclaimed once it no longer runs them", retained.forgotten, want)
	}
	if len(retained.forgetting) != 1 || retained.forgetting[0] != provider.ImageStore(pushed) {
		t.Errorf("the forget was handed %v, want the store the destroy was handed: an image in the project's registry is removed through it", retained.forgetting)
	}
}

func TestDestroyingAStackThatCannotForgetItsImagesFailsSoARetryReclaimsThem(t *testing.T) {
	t.Parallel()

	stacks, _, retained, spec := retainingContainers(t)
	ctx := context.Background()
	if _, err := stacks.Provision(ctx, spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	retained.refusal = errors.New("ecr:BatchDeleteImage is not granted")

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: spec.Ref}, progress.Discard()); !errors.Is(err, retained.refusal) {
		t.Errorf("Destroy() = %v, want the refusal: the record of the stack stays, and the next destroy reclaims what this one could not", err)
	}
}

func TestAContainerReleasePushesItsImageBeforeItProvisionsAnything(t *testing.T) {
	t.Parallel()

	stacks, engine, _, spec := retainingContainers(t)
	pushed := fake.NewImages()
	spec.Images.Store = pushed

	if _, err := stacks.Provision(context.Background(), spec, progress.Discard()); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	if got := pushed.Pushed(); len(got) != 1 || got[0].ImageRef != containerImage {
		t.Errorf("the release pushed %v, want its image to its ECR repository: the service pulls what the push left there", got)
	}
	if len(engine.stacks()) == 0 {
		t.Error("the release provisioned nothing")
	}
}

func TestAContainerReleaseWhoseImageCannotBePushedProvisionsNothing(t *testing.T) {
	t.Parallel()

	stacks, engine, _, spec := retainingContainers(t)
	refused := fake.NewImages()
	refused.FailPushes(errors.New("ecr:InitiateLayerUpload is not granted"))
	spec.Images.Store = refused

	if _, err := stacks.Provision(context.Background(), spec, progress.Discard()); err == nil {
		t.Fatal("Provision() succeeded with an image that never landed, so the service would pull a tag that is not there")
	}
	if ran := engine.stacks(); len(ran) != 0 {
		t.Errorf("the release ran %v, want nothing: an infrastructure stack made for an image that never landed idle-bills until the next deploy", ran)
	}
}

func containerDefinitionOf(cfg Config, spec provider.StackSpec) (*container.Definition, error) {
	work, err := container.NewDefinition(spec, cfg.containerTarget())
	if err != nil {
		return nil, err
	}
	work.Infra = fixtureContainerInfra()
	return work, nil
}

func hashedPriority(t *testing.T) int {
	t.Helper()
	work, err := containerDefinitionOf(containerStackSpec(t))
	if err != nil {
		t.Fatalf("containerDefinitionOf() = %v", err)
	}
	if err := work.PlaceRule(context.Background(), nil); err != nil {
		t.Fatalf("PlaceRule() = %v", err)
	}
	return work.Priority()
}
