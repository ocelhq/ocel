package resources_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type withContainers struct {
	*buckets
	provisioned []provider.StackSpec
	removed     []provider.AppContainer
}

func (w *withContainers) hooks() resources.Hooks {
	hooks := w.buckets.hooks()
	hooks.Containers = &resources.ContainerHooks{
		Provision: w.ProvisionContainers, Remove: w.RemoveContainers,
	}
	return hooks
}

func (w *withContainers) ProvisionContainers(_ context.Context, spec provider.StackSpec, _ progress.Log) ([]provider.AppContainer, error) {
	w.provisioned = append(w.provisioned, spec)
	return []provider.AppContainer{container(spec.Ref, spec.App.App)}, nil
}

func (w *withContainers) RemoveContainers(_ context.Context, _ provider.StackRef, containers []provider.AppContainer, _ provider.ImageStore, _ progress.Log) error {
	w.removed = append(w.removed, containers...)
	return nil
}

func container(ref provider.StackRef, name string) provider.AppContainer {
	return provider.AppContainer{Name: name, Physical: ref.Name.String() + "-" + name}
}

const testImage = "ocel/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func containerApp(app string) *provider.AppSpec {
	return &provider.AppSpec{
		App:             app,
		Compute:         provider.ComputeContainer,
		Image:           testImage,
		HealthCheckPath: "/healthz",
	}
}

func recordContainers(t *testing.T, store records.Store, ref provider.StackRef, names ...string) {
	t.Helper()

	stack := stackrecords.Stack{Kind: provider.StackApp}
	for _, name := range names {
		running := container(ref, name)
		running.Image = testImage
		stack.Containers = append(stack.Containers, running)
	}
	if err := stackrecords.Write(context.Background(), store, ref.Tier, ref.Project, ref.Name, stack); err != nil {
		t.Fatal(err)
	}
}

func TestAContainerAppReachesTheContainerPrimitiveWithItsImageAndProbe(t *testing.T) {
	t.Parallel()

	ref := appRef()
	own := &withContainers{buckets: &buckets{}}
	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), own.hooks())

	result, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackApp,
		App:  containerApp("web"),
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.provisioned) != 1 {
		t.Fatalf("the container primitive was called %d times, want the one app the spec names", len(own.provisioned))
	}
	app := own.provisioned[0].App
	if app.Compute != provider.ComputeContainer || app.Image != testImage || app.HealthCheckPath != "/healthz" {
		t.Errorf("the primitive read compute %q, image %q and probe %q, want the flat fields the spec has", app.Compute, app.Image, app.HealthCheckPath)
	}
	if len(result.Containers) != 1 || result.Containers[0].Name != "web" {
		t.Fatalf("Provision() returned %v, want the container the primitive provisioned", result.Containers)
	}
	if len(result.Functions) != 0 {
		t.Errorf("Provision() returned %v, want a container app to provision no function", result.Functions)
	}
}

func TestAServerlessAppStillReachesFunctions(t *testing.T) {
	t.Parallel()

	own := &withFunctions{buckets: &buckets{}}
	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), own.hooks())

	result, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:  appRef(),
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:       "web",
			Compute:   provider.ComputeServerless,
			Functions: []provider.FunctionSpec{{Name: "api"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Functions) != 1 || result.Functions[0].Name != "api" {
		t.Fatalf("Provision() returned %v, want the function the serverless app declares", result.Functions)
	}
	if len(result.Containers) != 0 {
		t.Errorf("Provision() returned %v, want a serverless app to provision no container", result.Containers)
	}
}

func TestAProviderProvisioningNoContainersRefusesAContainerAppByName(t *testing.T) {
	t.Parallel()

	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), (&withFunctions{buckets: &buckets{}}).hooks())

	_, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:  appRef(),
		Kind: provider.StackApp,
		App:  containerApp("web"),
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Provision() of a container app on a provider that provisions none = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refused.Message, "Containers hooks") {
		t.Errorf("the refusal reads %q, want it to name %s, the primitive this provider lacks", refused.Message, "Containers hooks")
	}
}

func TestAProviderProvisioningNoFunctionsRefusesAServerlessAppByName(t *testing.T) {
	t.Parallel()

	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), (&withContainers{buckets: &buckets{}}).hooks())

	_, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:  appRef(),
		Kind: provider.StackApp,
		App:  &provider.AppSpec{App: "web", Compute: provider.ComputeServerless},
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Provision() of a serverless app on a provider that provisions none = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refused.Message, "Functions hooks") {
		t.Errorf("the refusal reads %q, want it to name %s, the primitive this provider lacks", refused.Message, "Functions hooks")
	}
}

func TestAnAppNamingNoComputeIsRefusedRatherThanAssumedServerless(t *testing.T) {
	t.Parallel()

	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), (&withFunctions{buckets: &buckets{}}).hooks())

	_, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:  appRef(),
		Kind: provider.StackApp,
		App:  &provider.AppSpec{App: "web"},
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Provision() of an app naming no compute = %v, want an invalid refusal", err)
	}
}

func TestTheFanOutTakesDownTheContainerItsPlanShowsGoing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	recordContainers(t, store, ref, "web", "legacy")

	own := &withContainers{buckets: &buckets{}}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
	spec := provider.StackSpec{Ref: ref, Kind: provider.StackApp, App: containerApp("web")}

	shown, err := stacks.Plan(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	rows := rowsOf(shown)
	if rows["legacy"] != provider.ActionDelete {
		t.Fatalf("legacy reads %q, want the container this release stopped declaring shown as going", rows["legacy"])
	}
	if rows["web"] != provider.ActionKeep {
		t.Errorf("web reads %q, want the container this release still declares kept", rows["web"])
	}

	progress := &fake.Log{}
	if _, err := stacks.Provision(ctx, spec, progress); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "legacy" {
		t.Fatalf("the fan-out took down %v, want the legacy container its plan showed going", own.removed)
	}
	saysRemoving(t, progress, "container legacy")
}

func TestThePlanShowsAHeldContainerUpdatedWhenTheReleaseRunsAnotherImageAndKeptWhenItRunsTheSameOne(t *testing.T) {
	t.Parallel()

	ref := appRef()
	for recorded, want := range map[string]provider.ChangeAction{
		"ocel/web@sha256:b0": provider.ActionUpdate,
		testImage:            provider.ActionKeep,
	} {
		store := fake.NewKeyValues()
		running := provider.AppContainer{Name: "web", Physical: ref.Name.String() + "-web", Image: recorded}
		if err := stackrecords.Write(context.Background(), store, ref.Tier, ref.Project, ref.Name,
			stackrecords.Stack{Kind: provider.StackApp, Containers: []provider.AppContainer{running}}); err != nil {
			t.Fatal(err)
		}

		shown, err := resources.NewHookStacks(store, fake.NewArtifacts(), (&withContainers{buckets: &buckets{}}).hooks()).
			Plan(context.Background(), provider.StackSpec{Ref: ref, Kind: provider.StackApp, App: containerApp("web")}, nil)
		if err != nil {
			t.Fatalf("Plan() = %v", err)
		}
		if rows := rowsOf(shown); rows["web"] != want {
			t.Errorf("Plan() over a container running %s shows web %q, want %q", recorded, rows["web"], want)
		}
	}
}

func TestAProviderProvisioningNoContainersRefusesToOrphanTheOnesItRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	recordContainers(t, store, ref, "legacy")

	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), (&withFunctions{buckets: &buckets{}}).hooks())

	_, err := stacks.Provision(ctx, provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:       "web",
			Compute:   provider.ComputeServerless,
			Functions: []provider.FunctionSpec{{Name: "api"}},
		},
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "Containers hooks") {
		t.Fatalf("Provision() over a recorded container nothing can take down = %v, want a refusal naming %s", err, "Containers hooks")
	}
}

func TestDestroyTakesDownEveryContainerTheStackRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	recordContainers(t, store, ref, "web")

	own := &withContainers{buckets: &buckets{}}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())

	shown, err := stacks.PlanDestroy(ctx, ref, nil)
	if err != nil {
		t.Fatalf("PlanDestroy() = %v", err)
	}
	if rows := rowsOf(shown); rows["web"] != provider.ActionDelete {
		t.Errorf("web reads %q, want the recorded container shown as going", rows["web"])
	}
	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "web" {
		t.Fatalf("Destroy() took down %v, want the container the stack recorded", own.removed)
	}
}

func TestDestroyingAStackWhoseReleaseFailedForgetsTheImageItsRecordNames(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{Kind: provider.StackApp, App: "web", Image: testImage}); err != nil {
		t.Fatal(err)
	}
	forgot := 0
	own := &retaining{buckets: &buckets{}, forgetting: func() error { forgot++; return nil }}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())

	if err := stacks.Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}

	if forgot != 1 {
		t.Errorf("Destroy() forgot the stack's images %d times, want once: a release that failed recorded the image it pushed and no container, and nothing else ever reclaims that image", forgot)
	}
	if !slices.Contains(own.swept, "web "+testImage) {
		t.Errorf("Destroy() reconciled %v, want web's recorded image", own.swept)
	}
}

func TestRemovingPushedImagesLeavesOneARecordNamesByTheTimeItsTurnComes(t *testing.T) {
	t.Parallel()

	pushed := fake.NewImages()
	removed := 0
	recorded := func(context.Context) (map[string]bool, error) {
		if removed = len(pushed.Removed()); removed == 0 {
			return nil, nil
		}
		return map[string]bool{"r/shop.web:two": true}, nil
	}

	resources.RemovePushedImages(context.Background(), pushed, "web", []string{"r/shop.web:one", "r/shop.web:two"}, recorded, nil)

	if got, want := pushed.Removed(), []string{"r/shop.web:one"}; !slices.Equal(got, want) {
		t.Errorf("RemovePushedImages() removed %v, want %v: a deploy that recorded r/shop.web:two after the removal began found it pushed and will pull it", got, want)
	}
}

func TestDestroyRefusesByNameWhenNothingCanTakeTheRecordedContainerDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	recordContainers(t, store, ref, "web")

	err := resources.NewHookStacks(store, fake.NewArtifacts(), (&withFunctions{buckets: &buckets{}}).hooks()).Destroy(ctx, provider.StackDestroy{Ref: ref}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "Containers hooks") {
		t.Fatalf("Destroy() of a recorded container nothing provisions = %v, want a refusal naming %s", err, "Containers hooks")
	}
	if strings.Contains(refused.Message, "nothing here declares") {
		t.Errorf("the refusal reads %q, and a destroy declares nothing at all: the sentence an orphan sweep gives is false here", refused.Message)
	}
	if !strings.Contains(refused.Message, "this destroy would take down") {
		t.Errorf("the refusal reads %q, want it to say what a destroy is doing to the container", refused.Message)
	}
}

func TestAnAppMovingToAComputeThisProviderLacksIsRefusedBeforeItsFunctionsAreTakenDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	recordFunctions(t, store, ref, "api")

	own := &withFunctions{buckets: &buckets{}}

	_, err := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks()).Provision(ctx, provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackApp,
		App:  containerApp("web"),
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || !strings.Contains(refused.Message, "Containers hooks") {
		t.Fatalf("Provision() of a container app on a provider that provisions none = %v, want a refusal naming %s", err, "Containers hooks")
	}
	if len(own.removed) != 0 {
		t.Fatalf("the fan-out took down %v on a release it then refused, leaving the app down with nothing running in its place", own.removed)
	}
}

func TestAnAppNamingNoComputeIsRefusedBeforeItsContainerIsTakenDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	recordContainers(t, store, ref, "web")

	own := &withContainers{buckets: &buckets{}}

	_, err := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks()).Provision(ctx, provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackApp,
		App:  &provider.AppSpec{App: "web"},
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Provision() of an app naming no compute = %v, want an invalid refusal", err)
	}
	if len(own.removed) != 0 {
		t.Fatalf("the fan-out took down %v on a release it then refused, leaving the app down with nothing running in its place", own.removed)
	}
}

type misnaming struct{ *withContainers }

func (m misnaming) hooks() resources.Hooks {
	hooks := m.withContainers.hooks()
	hooks.Containers = &resources.ContainerHooks{Provision: m.ProvisionContainers, Remove: m.RemoveContainers}
	return hooks
}

func (m misnaming) ProvisionContainers(_ context.Context, spec provider.StackSpec, _ progress.Log) ([]provider.AppContainer, error) {
	m.provisioned = append(m.provisioned, spec)
	return []provider.AppContainer{container(spec.Ref, spec.App.App+"-svc")}, nil
}

func TestAContainerRunningUnderAnyNameButItsAppsIsSweptOnTheNextRelease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	own := &withContainers{buckets: &buckets{}}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), misnaming{own}.hooks())
	spec := provider.StackSpec{Ref: ref, Kind: provider.StackApp, App: containerApp("web")}

	result, err := stacks.Provision(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Containers) != 1 || result.Containers[0].Name != "web-svc" {
		t.Fatalf("Provision() returned %v, want the container this provider names for itself", result.Containers)
	}
	recordContainers(t, store, ref, result.Containers[0].Name)

	if _, err := stacks.Provision(ctx, spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "web-svc" {
		t.Fatalf("the fan-out took down %v; a release declares the app's own name and nothing else, so a container running under any other name is swept the next time round", own.removed)
	}
}
