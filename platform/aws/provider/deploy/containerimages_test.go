package deploy

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

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
		outputKeyContainerURL:      "http://" + fixtureOrigin,
		outputKeyContainerPhysical: "shop-prod-web-container-r3f8a1c90",
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

	_, _, _ = releasing(t, cfg).prepare(context.Background(), spec, runProvision)

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

	if err := stacks.Destroy(ctx, spec.Ref, pushed, progress.Discard()); err != nil {
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

	if err := stacks.Destroy(ctx, spec.Ref, nil, progress.Discard()); !errors.Is(err, retained.refusal) {
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
