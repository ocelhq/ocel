package resources_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type buckets struct {
	removed []provider.Binding
}

func (b *buckets) ProvisionBucket(_ context.Context, in resources.ProvisionRequest, _ edge.Progress) (provider.Binding, error) {
	return provider.Binding{
		Type:       provider.BindingBucket,
		Name:       in.Resource.Name,
		Properties: map[string]string{provider.PropertyBucket: in.Ref.Project + "-" + in.Resource.Name},
	}, nil
}

func (b *buckets) RemoveResource(_ context.Context, _ provider.StackRef, binding provider.Binding, _ edge.Progress) error {
	b.removed = append(b.removed, binding)
	return nil
}

func (b *buckets) hooks() resources.Hooks {
	return resources.Hooks{ProvisionBucket: b.ProvisionBucket, RemoveResource: b.RemoveResource}
}

type neon struct{ *buckets }

func (n neon) hooks() resources.Hooks {
	hooks := n.buckets.hooks()
	hooks.ProvisionPostgres = n.ProvisionPostgres
	return hooks
}

func (neon) ProvisionPostgres(_ context.Context, in resources.ProvisionRequest, _ edge.Progress) (provider.Binding, error) {
	return provider.Binding{
		Type: provider.BindingPostgres,
		Name: in.Resource.Name,
		Properties: map[string]string{
			provider.PropertyHost:     "db.neon.invalid",
			provider.PropertyPort:     "5432",
			provider.PropertyDatabase: in.Resource.Name,
			provider.PropertyUsername: "app",
			provider.PropertyPassword: "hunter2",
		},
	}, nil
}

type halfBinding struct{}

func (h halfBinding) hooks() resources.Hooks {
	return resources.Hooks{ProvisionPostgres: h.ProvisionPostgres}
}

func (halfBinding) ProvisionPostgres(_ context.Context, in resources.ProvisionRequest, _ edge.Progress) (provider.Binding, error) {
	return provider.Binding{
		Type:       provider.BindingPostgres,
		Name:       in.Resource.Name,
		Properties: map[string]string{provider.PropertyHost: "db.invalid"},
	}, nil
}

func infraRef() provider.StackRef {
	return provider.StackRef{
		Project: "shop",
		Class:   edge.ClassProduction,
		Name:    naming.InfraStack("prod"),
	}
}

func TestServesNamesEveryResourceTheHooksProvision(t *testing.T) {
	t.Parallel()

	if served := resources.ServedBindingTypes(resources.Hooks{}); len(served) != 0 {
		t.Fatalf("ServedBindingTypes() = %v for hooks that provision nothing, want nothing", served)
	}
	if served := resources.ServedBindingTypes((&buckets{}).hooks()); !slices.Equal(served, []provider.BindingType{provider.BindingBucket}) {
		t.Fatalf("ServedBindingTypes() = %v, want only the bucket the hooks provision", served)
	}
	served := resources.ServedBindingTypes(neon{&buckets{}}.hooks())
	if !slices.Equal(served, []provider.BindingType{provider.BindingPostgres, provider.BindingBucket}) {
		t.Fatalf("ServedBindingTypes() = %v, want the Postgres and the bucket the hooks provision", served)
	}
	removing := resources.Hooks{RemoveResource: (&buckets{}).RemoveResource}
	if served := resources.ServedBindingTypes(removing); len(served) != 0 {
		t.Fatalf("ServedBindingTypes() = %v for hooks that only remove, want nothing: a resource nothing can provision is not served", served)
	}
}

func TestReleaserFansEachResourceOutToItsPrimitive(t *testing.T) {
	t.Parallel()

	store := fake.NewRecords()
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), neon{&buckets{}}.hooks())

	result, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:  infraRef(),
		Kind: provider.StackInfra,
		Resources: []provider.Resource{
			{Name: "orders", Type: provider.BindingPostgres},
			{Name: "uploads", Type: provider.BindingBucket},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Bindings) != 2 {
		t.Fatalf("Provision() returned %d bindings, want one per resource", len(result.Bindings))
	}
	if result.Bindings[0].Properties[provider.PropertyHost] != "db.neon.invalid" {
		t.Errorf("orders came back from %q, want the override that serves Postgres", result.Bindings[0].Properties[provider.PropertyHost])
	}
}

func TestReleaserRefusesAPrimitiveNothingServes(t *testing.T) {
	t.Parallel()

	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), (&buckets{}).hooks())

	_, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:       infraRef(),
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "orders", Type: provider.BindingPostgres}},
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Provision() of a primitive nothing serves = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refused.Message, string(provider.BindingBucket)) {
		t.Errorf("the refusal reads %q, want it to name what this provider does serve", refused.Message)
	}
}

func TestPlanRefusesAPrimitiveNothingServes(t *testing.T) {
	t.Parallel()

	_, err := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), (&buckets{}).hooks()).Plan(context.Background(), provider.StackSpec{
		Ref:       infraRef(),
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "orders", Type: provider.BindingPostgres}},
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Plan() of a primitive nothing serves = %v, want the refusal the provision would give", err)
	}
}

func TestReleaserRefusesABindingMissingAPropertyItsTypePromises(t *testing.T) {
	t.Parallel()

	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), halfBinding{}.hooks())

	_, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:       infraRef(),
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "orders", Type: provider.BindingPostgres}},
	}, nil)
	if err == nil {
		t.Fatal("Provision() recorded a Postgres binding with only a host, want it refused before anything binds to it")
	}
	if !strings.Contains(err.Error(), provider.PropertyDatabase) {
		t.Errorf("the refusal reads %q, want it to name the property that is missing", err)
	}
}

func TestReleaserRemovesAResourceTheSpecNoLongerDeclares(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	own := &buckets{}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
	ref := infraRef()

	if err := stackrecords.Write(ctx, store, ref.Class, ref.Project, ref.Name, stackrecords.Stack{
		Kind: provider.StackInfra,
		Bindings: []provider.Binding{
			{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{provider.PropertyBucket: "shop-uploads"}},
			{Type: provider.BindingBucket, Name: "exports", Properties: map[string]string{provider.PropertyBucket: "shop-exports"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := stacks.Provision(ctx, provider.StackSpec{
		Ref:       ref,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "uploads", Type: provider.BindingBucket}},
	}, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	if len(own.removed) != 1 || own.removed[0].Name != "exports" {
		t.Fatalf("the fan-out removed %v, want only the export bucket this spec stopped declaring", own.removed)
	}
}

func TestDestroyOfAStackNothingRecordedIsANoOp(t *testing.T) {
	t.Parallel()

	own := &buckets{}
	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), own.hooks())

	if err := stacks.Destroy(context.Background(), infraRef(), nil); err != nil {
		t.Fatalf("Destroy() of a stack nothing recorded = %v, want nil", err)
	}
	if len(own.removed) != 0 {
		t.Errorf("Destroy() removed %v for a stack that was never provisioned", own.removed)
	}
}

func TestDestroyTakesDownEveryBindingTheStackRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	own := &buckets{}
	ref := infraRef()

	if err := stackrecords.Write(ctx, store, ref.Class, ref.Project, ref.Name, stackrecords.Stack{
		Kind:     provider.StackInfra,
		Bindings: []provider.Binding{{Type: provider.BindingBucket, Name: "uploads"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks()).Destroy(ctx, ref, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.removed) != 1 {
		t.Fatalf("Destroy() removed %d bindings, want the one the stack recorded", len(own.removed))
	}
}

func rowsOf(plan provider.Plan) map[string]provider.ChangeAction {
	rows := map[string]provider.ChangeAction{}
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			rows[change.Name] = change.Action
		}
	}
	return rows
}

func TestPlanOverAStackNothingRecordedCreatesEveryResourceItDeclares(t *testing.T) {
	t.Parallel()

	plan, err := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), neon{&buckets{}}.hooks()).Plan(context.Background(), provider.StackSpec{
		Ref:  infraRef(),
		Kind: provider.StackInfra,
		Resources: []provider.Resource{
			{Name: "orders", Type: provider.BindingPostgres},
			{Name: "uploads", Type: provider.BindingBucket},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("Plan() returned %d groups, want the one stack it releases", len(plan.Groups))
	}
	if plan.Groups[0].Action != provider.ActionCreate {
		t.Errorf("the group reads %q, want the stack shown as a create", plan.Groups[0].Action)
	}
	rows := rowsOf(plan)
	for _, name := range []string{"orders", "uploads"} {
		if rows[name] != provider.ActionCreate {
			t.Errorf("%s reads %q, want it created: nothing is recorded yet", name, rows[name])
		}
	}
}

func TestPlanKeepsWhatExistsAndDeletesWhatThePlanDropped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	ref := infraRef()
	if err := stackrecords.Write(ctx, store, ref.Class, ref.Project, ref.Name, stackrecords.Stack{
		Kind: provider.StackInfra,
		Bindings: []provider.Binding{
			{Type: provider.BindingBucket, Name: "uploads"},
			{Type: provider.BindingBucket, Name: "exports"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := resources.NewHookStacks(store, fake.NewArtifacts(), (&buckets{}).hooks()).Plan(ctx, provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackInfra,
		Resources: []provider.Resource{
			{Name: "uploads", Type: provider.BindingBucket},
			{Name: "invoices", Type: provider.BindingBucket},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	rows := rowsOf(plan)
	want := map[string]provider.ChangeAction{
		"uploads":  provider.ActionKeep,
		"invoices": provider.ActionCreate,
		"exports":  provider.ActionDelete,
	}
	for name, action := range want {
		if rows[name] != action {
			t.Errorf("%s reads %q, want %q", name, rows[name], action)
		}
	}
	for name, action := range rows {
		if action == provider.ActionUpdate {
			t.Errorf("%s reads as an update, and a fan-out with no engine cannot know a resource changed", name)
		}
	}
}

func TestPlanDestroyTakesDownEveryBindingTheStackRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	ref := infraRef()
	if err := stackrecords.Write(ctx, store, ref.Class, ref.Project, ref.Name, stackrecords.Stack{
		Kind:     provider.StackInfra,
		Bindings: []provider.Binding{{Type: provider.BindingBucket, Name: "uploads"}},
	}); err != nil {
		t.Fatal(err)
	}

	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), (&buckets{}).hooks())
	plan, err := stacks.PlanDestroy(ctx, ref, nil)
	if err != nil {
		t.Fatalf("PlanDestroy() = %v", err)
	}
	if rows := rowsOf(plan); rows["uploads"] != provider.ActionDelete {
		t.Errorf("uploads reads %q, want the recorded binding shown as going", rows["uploads"])
	}

	absent := ref
	absent.Name = naming.InfraStack("never-provisioned")
	empty, err := stacks.PlanDestroy(ctx, absent, nil)
	if err != nil {
		t.Fatalf("PlanDestroy() of a stack nothing recorded = %v", err)
	}
	if len(empty.Groups) != 0 {
		t.Errorf("PlanDestroy() of a stack nothing recorded returned %+v, want nothing to take down", empty.Groups)
	}
}

type withFunctions struct {
	*buckets
	removed []provider.Function
}

func (w *withFunctions) hooks() resources.Hooks {
	hooks := w.buckets.hooks()
	hooks.Functions = &resources.FunctionHooks{Provision: w.ProvisionFunctions, Remove: w.RemoveFunctions}
	return hooks
}

func (w *withFunctions) ProvisionFunctions(_ context.Context, spec provider.StackSpec, _ edge.Progress) ([]provider.Function, error) {
	var functions []provider.Function
	for _, fn := range spec.App.Functions {
		functions = append(functions, function(spec.Ref, fn.Name))
	}
	return functions, nil
}

func (w *withFunctions) RemoveFunctions(_ context.Context, _ provider.StackRef, functions []provider.Function, _ edge.Progress) error {
	w.removed = append(w.removed, functions...)
	return nil
}

func appRef() provider.StackRef {
	return provider.StackRef{
		Project: "shop",
		Class:   edge.ClassProduction,
		Name:    naming.AppStack("prod", "web", naming.NewRelease("d1", "f1")),
	}
}

func function(ref provider.StackRef, name string) provider.Function {
	return provider.Function{Name: name, Physical: ref.Name.String() + "-" + name}
}

func recordFunctions(t *testing.T, store records.Store, ref provider.StackRef, names ...string) {
	t.Helper()

	stack := stackrecords.Stack{Kind: provider.StackApp}
	for _, name := range names {
		stack.Functions = append(stack.Functions, function(ref, name))
	}
	if err := stackrecords.Write(context.Background(), store, ref.Class, ref.Project, ref.Name, stack); err != nil {
		t.Fatal(err)
	}
}

func TestTheFanOutTakesDownTheFunctionItsPlanShowsGoing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	ref := appRef()
	recordFunctions(t, store, ref, "api", "legacy")

	own := &withFunctions{buckets: &buckets{}}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
	spec := provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:       "web",
			Compute:   provider.ComputeServerless,
			Functions: []provider.FunctionSpec{{Name: "api"}},
		},
	}

	shown, err := stacks.Plan(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if rows := rowsOf(shown); rows["legacy"] != provider.ActionDelete {
		t.Fatalf("legacy reads %q, want the function this release stopped declaring shown as going", rows["legacy"])
	}

	if _, err := stacks.Provision(ctx, spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "legacy" {
		t.Fatalf("the fan-out took down %v, want the legacy function its plan showed going", own.removed)
	}
}

func TestAReleaseDeclaringNoAppTakesDownTheFunctionsItsPlanShowsGoing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	ref := appRef()
	recordFunctions(t, store, ref, "api")

	own := &withFunctions{buckets: &buckets{}}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
	spec := provider.StackSpec{Ref: ref, Kind: provider.StackInfra}

	shown, err := stacks.Plan(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if rows := rowsOf(shown); rows["api"] != provider.ActionDelete {
		t.Fatalf("api reads %q, want the function no release declares shown as going", rows["api"])
	}

	if _, err := stacks.Provision(ctx, spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "api" {
		t.Fatalf("the fan-out took down %v, want the function its plan showed going", own.removed)
	}
}

type withContainers struct {
	*buckets
	provisioned []provider.StackSpec
	removed     []provider.AppContainer
}

func (w *withContainers) hooks() resources.Hooks {
	hooks := w.buckets.hooks()
	hooks.Containers = &resources.ContainerHooks{Provision: w.ProvisionContainers, Remove: w.RemoveContainers}
	return hooks
}

func (w *withContainers) ProvisionContainers(_ context.Context, spec provider.StackSpec, _ edge.Progress) ([]provider.AppContainer, error) {
	w.provisioned = append(w.provisioned, spec)
	return []provider.AppContainer{container(spec.Ref, spec.App.App)}, nil
}

func (w *withContainers) RemoveContainers(_ context.Context, _ provider.StackRef, containers []provider.AppContainer, _ edge.Progress) error {
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
		stack.Containers = append(stack.Containers, container(ref, name))
	}
	if err := stackrecords.Write(context.Background(), store, ref.Class, ref.Project, ref.Name, stack); err != nil {
		t.Fatal(err)
	}
}

func TestAContainerAppReachesTheContainerPrimitiveWithItsImageAndProbe(t *testing.T) {
	t.Parallel()

	ref := appRef()
	own := &withContainers{buckets: &buckets{}}
	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), own.hooks())

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
	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), own.hooks())

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

	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), (&withFunctions{buckets: &buckets{}}).hooks())

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

	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), (&withContainers{buckets: &buckets{}}).hooks())

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

	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), (&withFunctions{buckets: &buckets{}}).hooks())

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
	store := fake.NewRecords()
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

	if _, err := stacks.Provision(ctx, spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "legacy" {
		t.Fatalf("the fan-out took down %v, want the legacy container its plan showed going", own.removed)
	}
}

func TestAProviderProvisioningNoContainersRefusesToOrphanTheOnesItRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
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
	store := fake.NewRecords()
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
	if err := stacks.Destroy(ctx, ref, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "web" {
		t.Fatalf("Destroy() took down %v, want the container the stack recorded", own.removed)
	}
}

func TestDestroyRefusesByNameWhenNothingCanTakeTheRecordedContainerDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	ref := appRef()
	recordContainers(t, store, ref, "web")

	err := resources.NewHookStacks(store, fake.NewArtifacts(), (&withFunctions{buckets: &buckets{}}).hooks()).Destroy(ctx, ref, nil)
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
	store := fake.NewRecords()
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
	store := fake.NewRecords()
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

func (m misnaming) ProvisionContainers(_ context.Context, spec provider.StackSpec, _ edge.Progress) ([]provider.AppContainer, error) {
	m.provisioned = append(m.provisioned, spec)
	return []provider.AppContainer{container(spec.Ref, spec.App.App+"-svc")}, nil
}

func TestAContainerRunningUnderAnyNameButItsAppsIsSweptOnTheNextRelease(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
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

func imagePushes(store provider.ImageStore) provider.ImagePushes {
	return provider.ImagePushes{Store: store, Pushes: []provider.ImagePush{{
		App:      "web",
		Source:   testImage,
		ImageRef: "ghcr.io/acme/web:sha256-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Digest:   "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}}
}

func TestTheImageIsPushedBeforeTheContainerItIsProvisionedFrom(t *testing.T) {
	t.Parallel()

	own := &withContainers{buckets: &buckets{}}
	registry := fake.NewImages()
	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), own.hooks())
	spec := provider.StackSpec{
		Ref:    appRef(),
		Kind:   provider.StackApp,
		App:    containerApp("web"),
		Images: imagePushes(registry),
	}

	if _, err := stacks.Provision(context.Background(), spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if pushed := registry.Pushed(); len(pushed) != 1 || pushed[0].Source != testImage {
		t.Fatalf("the fan-out pushed %v, want the app's own image", pushed)
	}
	if len(own.provisioned) != 1 {
		t.Fatalf("the fan-out provisioned %d containers, want the one the pushed image runs", len(own.provisioned))
	}
}

func TestAReleaseWhoseImageCannotBePushedProvisionsNothing(t *testing.T) {
	t.Parallel()

	own := &withContainers{buckets: &buckets{}}
	registry := fake.NewImages()
	registry.FailPushes(errors.New("the registry refused the token"))
	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), own.hooks())
	spec := provider.StackSpec{
		Ref:    appRef(),
		Kind:   provider.StackApp,
		App:    containerApp("web"),
		Images: imagePushes(registry),
	}

	if _, err := stacks.Provision(context.Background(), spec, nil); err == nil {
		t.Fatal("Provision() succeeded with an image that never reached the registry, so the box would be pointed at an image it cannot pull")
	}
	if len(own.provisioned) != 0 {
		t.Fatalf("the fan-out provisioned %v after the push failed", own.provisioned)
	}
}

type retaining struct {
	*buckets
	swept        []string
	taken        []string
	window       map[string][]string
	retained     map[string]bool
	provisioning func() error
	served       func(resources.ProvisionRequest) error
	sweeping     func() error
	forgetting   func() error
}

func (r *retaining) promote(app, imageRef string) {
	if r.window == nil {
		r.window = map[string][]string{}
	}
	if r.retained == nil {
		r.retained = map[string]bool{}
	}
	r.window[app] = append([]string{imageRef}, r.window[app]...)
	r.retained[imageRef] = true
}

func (r *retaining) hooks() resources.Hooks {
	hooks := r.buckets.hooks()
	hooks.ProvisionBucket = r.ProvisionBucket
	hooks.Containers = &resources.ContainerHooks{Provision: r.ProvisionContainers, Remove: r.RemoveContainers}
	hooks.Retention = &resources.ImageRetentionHooks{Reconcile: r.ReconcileImages, Forget: r.ForgetReleases}
	return hooks
}

func (r *retaining) retains(imageRef string) bool { return r.retained[imageRef] }

func (r *retaining) ProvisionContainers(_ context.Context, spec provider.StackSpec, _ edge.Progress) ([]provider.AppContainer, error) {
	if r.provisioning != nil {
		if err := r.provisioning(); err != nil {
			return nil, err
		}
	}
	r.promote(spec.App.App, spec.App.Image)
	return []provider.AppContainer{{Name: spec.App.App, Physical: spec.Ref.Name.String() + "-" + spec.App.App, Image: spec.App.Image}}, nil
}

func (r *retaining) RemoveContainers(_ context.Context, _ provider.StackRef, going []provider.AppContainer, _ edge.Progress) error {
	for _, container := range going {
		r.taken = append(r.taken, container.Physical)
	}
	return nil
}

func (r *retaining) ProvisionBucket(ctx context.Context, in resources.ProvisionRequest, progress edge.Progress) (provider.Binding, error) {
	if r.served != nil {
		if err := r.served(in); err != nil {
			return provider.Binding{}, err
		}
	}
	return r.buckets.ProvisionBucket(ctx, in, progress)
}

func (r *retaining) ReconcileImages(_ context.Context, _ provider.StackRef, app, imageRef string, _ edge.Progress) error {
	r.swept = append(r.swept, app+" "+imageRef)
	if r.sweeping != nil {
		if err := r.sweeping(); err != nil {
			return err
		}
	}
	for ref := range r.retained {
		if !slices.Contains(r.window[app], ref) {
			delete(r.retained, ref)
		}
	}
	return nil
}

func (r *retaining) ForgetReleases(_ context.Context, _ provider.StackRef, app string, _ edge.Progress) error {
	if r.forgetting != nil {
		if err := r.forgetting(); err != nil {
			return err
		}
	}
	delete(r.window, app)
	return nil
}

type refusingImages struct{ err error }

func (r refusingImages) Has(context.Context, provider.ImagePush) (bool, error) { return false, nil }

func (refusingImages) Destination() string { return "the refusing registry" }

func (r refusingImages) Push(context.Context, provider.ImagePush, edge.Progress) error {
	return r.err
}

func refusingPlan(err error) provider.ImagePushes {
	return provider.ImagePushes{
		Store:  refusingImages{err: err},
		Pushes: []provider.ImagePush{{App: "web", ImageRef: testImage}},
	}
}

func TestAContainerReleaseReconcilesItsImagesOnEveryPathOutOfProvision(t *testing.T) {
	t.Parallel()

	refused := errors.New("refused")
	for name, breaking := range map[string]func(*retaining) provider.StackSpec{
		"the image never lands": func(own *retaining) provider.StackSpec {
			spec := containerSpec()
			spec.Images = refusingPlan(refused)
			return spec
		},
		"a resource never provisions": func(own *retaining) provider.StackSpec {
			own.served = func(resources.ProvisionRequest) error { return refused }
			spec := containerSpec()
			spec.Resources = []provider.Resource{{Name: "store", Type: provider.BindingBucket}}
			return spec
		},
		"the container is never provisioned": func(own *retaining) provider.StackSpec {
			own.provisioning = func() error { return refused }
			return containerSpec()
		},
		"the release succeeds": func(own *retaining) provider.StackSpec {
			return containerSpec()
		},
	} {
		t.Run(name, func(t *testing.T) {
			own := &retaining{buckets: &buckets{}}
			spec := breaking(own)
			stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), own.hooks())

			_, err := stacks.Provision(context.Background(), spec, nil)
			if name != "the release succeeds" && err == nil {
				t.Fatal("Provision() succeeded, and this case is the failure path")
			}
			if len(own.swept) != 1 || own.swept[0] != "web "+testImage {
				t.Fatalf("the release swept %v, want one reconcile of web: the box is agentless, so a deploy that fails and is never retried must not leave it dirtier than it found it", own.swept)
			}
		})
	}
}

type unreadable struct{ *fake.Records }

func (unreadable) Read(context.Context, records.Name) (records.Record, error) {
	return records.Record{}, errors.New("this login reads no record tier")
}

func TestAContainerReleaseReconcilesEvenWhenItNeverReachedTheWork(t *testing.T) {
	t.Parallel()

	for name, breaking := range map[string]func() (records.Store, provider.StackSpec){
		"the record cannot be read": func() (records.Store, provider.StackSpec) {
			return unreadable{fake.NewRecords()}, containerSpec()
		},
		"a resource names a primitive this provider never serves": func() (records.Store, provider.StackSpec) {
			spec := containerSpec()
			spec.Resources = []provider.Resource{{Name: "ledger", Type: provider.BindingPostgres}}
			return fake.NewRecords(), spec
		},
		"the app names a compute nothing provisions": func() (records.Store, provider.StackSpec) {
			spec := containerSpec()
			spec.App.Compute = provider.Compute("steam")
			return fake.NewRecords(), spec
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			own := &retaining{buckets: &buckets{}}
			store, spec := breaking()
			stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())

			if _, err := stacks.Provision(context.Background(), spec, nil); err == nil {
				t.Fatal("Provision() succeeded, and this case is the failure path")
			}
			if len(own.swept) != 1 || own.swept[0] != "web "+testImage {
				t.Fatalf("the release swept %v, want one reconcile of web: the sweep is deferred rather than conditional, so a release that leaves before it reaches the work sweeps its own leaked image on the way out", own.swept)
			}
		})
	}
}

func TestAServerlessReleaseReconcilesNoImages(t *testing.T) {
	t.Parallel()

	own := &retaining{buckets: &buckets{}}
	stacks := resources.NewHookStacks(fake.NewRecords(), fake.NewArtifacts(), own.hooks())

	spec := containerSpec()
	spec.App = nil
	if _, err := stacks.Provision(context.Background(), spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.swept) != 0 {
		t.Errorf("the release swept %v over a spec that runs no image at all", own.swept)
	}
}

func TestATeardownSweepsTheImageTheContainerItTookDownWasRetaining(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewRecords()
	ref := appRef()
	if err := stackrecords.Write(ctx, store, ref.Class, ref.Project, ref.Name, stackrecords.Stack{
		Kind:       provider.StackApp,
		Containers: []provider.AppContainer{{Name: "web", Physical: "shop-prod-web", Image: testImage}},
	}); err != nil {
		t.Fatal(err)
	}

	own := &retaining{buckets: &buckets{}}
	own.promote("web", testImage)
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
	if err := stacks.Destroy(ctx, ref, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.swept) != 1 || own.swept[0] != "web "+testImage {
		t.Fatalf("the teardown swept %v, want the image the container it took down was the last thing referencing", own.swept)
	}
	if own.retains(testImage) {
		t.Error("the teardown left " + testImage + " on the box, and a stack that names itself in the window it never rewrites is swept by nothing that comes after it")
	}
}

func TestATeardownThatStoppedReconcilingSaysSoWithNoProgressListening(t *testing.T) {
	t.Parallel()

	refused := errors.New("the helper is not on this box")
	for name, breaking := range map[string]func(*retaining){
		"the window was never dropped": func(own *retaining) { own.forgetting = func() error { return refused } },
		"the sweep never ran":          func(own *retaining) { own.sweeping = func() error { return refused } },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store := fake.NewRecords()
			ref := appRef()
			if err := stackrecords.Write(ctx, store, ref.Class, ref.Project, ref.Name, stackrecords.Stack{
				Kind:       provider.StackApp,
				Containers: []provider.AppContainer{{Name: "web", Physical: "shop-prod-web", Image: testImage}},
			}); err != nil {
				t.Fatal(err)
			}

			own := &retaining{buckets: &buckets{}}
			breaking(own)
			stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())

			err := stacks.Destroy(ctx, ref, nil)
			if !errors.Is(err, refused) {
				t.Errorf("Destroy() = %v, want %v: a destroy run with no reporter attached is where the only trace of a box that stopped reconciling would be lost", err, refused)
			}
			if !slices.Equal(own.taken, []string{"shop-prod-web"}) {
				t.Errorf("the teardown took down %v, want the container it recorded: the sweep failing is reported after the removals, never instead of them", own.taken)
			}
		})
	}
}

func containerSpec() provider.StackSpec {
	return provider.StackSpec{Ref: appRef(), Kind: provider.StackApp, App: containerApp("web")}
}
