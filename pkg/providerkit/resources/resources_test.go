package resources_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
)

type buckets struct {
	removed []providerkit.Link
}

func (b *buckets) Bucket(_ context.Context, in resources.Instruction, _ providerkit.Reporter) (providerkit.Link, error) {
	return providerkit.Link{
		Type:       providerkit.LinkBucket,
		Name:       in.Resource.Name,
		Properties: map[string]string{providerkit.PropertyBucket: in.Ref.Project + "-" + in.Resource.Name},
	}, nil
}

func (b *buckets) RemoveResource(_ context.Context, _ providerkit.StackRef, link providerkit.Link, _ providerkit.Reporter) error {
	b.removed = append(b.removed, link)
	return nil
}

type neon struct{ *buckets }

func (neon) Postgres(_ context.Context, in resources.Instruction, _ providerkit.Reporter) (providerkit.Link, error) {
	return providerkit.Link{
		Type: providerkit.LinkPostgres,
		Name: in.Resource.Name,
		Properties: map[string]string{
			providerkit.PropertyHost:     "db.neon.invalid",
			providerkit.PropertyPort:     "5432",
			providerkit.PropertyDatabase: in.Resource.Name,
			providerkit.PropertyUsername: "app",
			providerkit.PropertyPassword: "hunter2",
		},
	}, nil
}

type halfLink struct{}

func (halfLink) Postgres(_ context.Context, in resources.Instruction, _ providerkit.Reporter) (providerkit.Link, error) {
	return providerkit.Link{
		Type:       providerkit.LinkPostgres,
		Name:       in.Resource.Name,
		Properties: map[string]string{providerkit.PropertyHost: "db.invalid"},
	}, nil
}

func infraRef() providerkit.StackRef {
	return providerkit.StackRef{
		Project: "shop",
		Class:   providerkit.ClassProduction,
		Name:    naming.InfraStack("prod"),
	}
}

func TestServesNamesEveryPrimitiveTheProviderImplements(t *testing.T) {
	t.Parallel()

	if served := resources.Serves(&buckets{}); !slices.Equal(served, []providerkit.LinkType{providerkit.LinkBucket}) {
		t.Fatalf("Serves() = %v, want only the bucket it implements", served)
	}
	served := resources.Serves(neon{&buckets{}})
	if !slices.Contains(served, providerkit.LinkPostgres) || !slices.Contains(served, providerkit.LinkBucket) {
		t.Fatalf("Serves() = %v, want the embedded bucket and the Postgres the override adds", served)
	}
}

func TestReleaserFansEachResourceOutToItsPrimitive(t *testing.T) {
	t.Parallel()

	records := fake.NewRecords()
	releaser := resources.Releaser(records, fake.NewArtifacts(), neon{&buckets{}})

	result, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:  infraRef(),
		Kind: providerkit.StackInfra,
		Resources: []providerkit.Resource{
			{Name: "orders", Type: providerkit.LinkPostgres},
			{Name: "uploads", Type: providerkit.LinkBucket},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Links) != 2 {
		t.Fatalf("Provision() returned %d links, want one per resource", len(result.Links))
	}
	if result.Links[0].Properties[providerkit.PropertyHost] != "db.neon.invalid" {
		t.Errorf("orders came back from %q, want the override that serves Postgres", result.Links[0].Properties[providerkit.PropertyHost])
	}
}

func TestReleaserRefusesAPrimitiveNothingServes(t *testing.T) {
	t.Parallel()

	releaser := resources.Releaser(fake.NewRecords(), fake.NewArtifacts(), &buckets{})

	_, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:       infraRef(),
		Kind:      providerkit.StackInfra,
		Resources: []providerkit.Resource{{Name: "orders", Type: providerkit.LinkPostgres}},
	}, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Provision() of a primitive nothing serves = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refusal.Message, string(providerkit.LinkBucket)) {
		t.Errorf("the refusal reads %q, want it to name what this provider does serve", refusal.Message)
	}
}

func TestPlanRefusesAPrimitiveNothingServes(t *testing.T) {
	t.Parallel()

	_, err := resources.Releaser(fake.NewRecords(), fake.NewArtifacts(), &buckets{}).Plan(context.Background(), providerkit.StackPlan{
		Ref:       infraRef(),
		Kind:      providerkit.StackInfra,
		Resources: []providerkit.Resource{{Name: "orders", Type: providerkit.LinkPostgres}},
	}, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Plan() of a primitive nothing serves = %v, want the refusal the provision would give", err)
	}
}

func TestReleaserRefusesALinkMissingAPropertyItsTypePromises(t *testing.T) {
	t.Parallel()

	releaser := resources.Releaser(fake.NewRecords(), fake.NewArtifacts(), halfLink{})

	_, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:       infraRef(),
		Kind:      providerkit.StackInfra,
		Resources: []providerkit.Resource{{Name: "orders", Type: providerkit.LinkPostgres}},
	}, nil)
	if err == nil {
		t.Fatal("Provision() recorded a Postgres link carrying only a host, want it refused before anything binds to it")
	}
	if !strings.Contains(err.Error(), providerkit.PropertyDatabase) {
		t.Errorf("the refusal reads %q, want it to name the property that is missing", err)
	}
}

func TestReleaserRemovesAResourceThePlanNoLongerDeclares(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	own := &buckets{}
	releaser := resources.Releaser(records, fake.NewArtifacts(), own)
	ref := infraRef()

	if err := providerkit.WriteStack(ctx, records, ref.Class, ref.Project, ref.Name, providerkit.Stack{
		Kind: providerkit.StackInfra,
		Links: []providerkit.Link{
			{Type: providerkit.LinkBucket, Name: "uploads", Properties: map[string]string{providerkit.PropertyBucket: "shop-uploads"}},
			{Type: providerkit.LinkBucket, Name: "exports", Properties: map[string]string{providerkit.PropertyBucket: "shop-exports"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := releaser.Provision(ctx, providerkit.StackPlan{
		Ref:       ref,
		Kind:      providerkit.StackInfra,
		Resources: []providerkit.Resource{{Name: "uploads", Type: providerkit.LinkBucket}},
	}, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	if len(own.removed) != 1 || own.removed[0].Name != "exports" {
		t.Fatalf("the fan-out removed %v, want only the export bucket this plan stopped declaring", own.removed)
	}
}

func TestDestroyOfAStackNothingRecordedIsANoOp(t *testing.T) {
	t.Parallel()

	own := &buckets{}
	releaser := resources.Releaser(fake.NewRecords(), fake.NewArtifacts(), own)

	if err := releaser.Destroy(context.Background(), infraRef(), nil); err != nil {
		t.Fatalf("Destroy() of a stack nothing recorded = %v, want nil", err)
	}
	if len(own.removed) != 0 {
		t.Errorf("Destroy() removed %v for a stack that was never provisioned", own.removed)
	}
}

func TestDestroyTakesDownEveryLinkTheStackRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	own := &buckets{}
	ref := infraRef()

	if err := providerkit.WriteStack(ctx, records, ref.Class, ref.Project, ref.Name, providerkit.Stack{
		Kind:  providerkit.StackInfra,
		Links: []providerkit.Link{{Type: providerkit.LinkBucket, Name: "uploads"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := resources.Releaser(records, fake.NewArtifacts(), own).Destroy(ctx, ref, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.removed) != 1 {
		t.Fatalf("Destroy() removed %d links, want the one the stack recorded", len(own.removed))
	}
}

func rowsOf(plan providerkit.Plan) map[string]providerkit.ChangeAction {
	rows := map[string]providerkit.ChangeAction{}
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			rows[change.Name] = change.Action
		}
	}
	return rows
}

func TestPlanOverAStackNothingRecordedCreatesEveryResourceItDeclares(t *testing.T) {
	t.Parallel()

	plan, err := resources.Releaser(fake.NewRecords(), fake.NewArtifacts(), neon{&buckets{}}).Plan(context.Background(), providerkit.StackPlan{
		Ref:  infraRef(),
		Kind: providerkit.StackInfra,
		Resources: []providerkit.Resource{
			{Name: "orders", Type: providerkit.LinkPostgres},
			{Name: "uploads", Type: providerkit.LinkBucket},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("Plan() returned %d groups, want the one stack it releases", len(plan.Groups))
	}
	if plan.Groups[0].Action != providerkit.ActionCreate {
		t.Errorf("the group reads %q, want the stack shown as a create", plan.Groups[0].Action)
	}
	rows := rowsOf(plan)
	for _, name := range []string{"orders", "uploads"} {
		if rows[name] != providerkit.ActionCreate {
			t.Errorf("%s reads %q, want it created: nothing recorded stands", name, rows[name])
		}
	}
}

func TestPlanKeepsWhatStandsAndDeletesWhatThePlanDropped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	ref := infraRef()
	if err := providerkit.WriteStack(ctx, records, ref.Class, ref.Project, ref.Name, providerkit.Stack{
		Kind: providerkit.StackInfra,
		Links: []providerkit.Link{
			{Type: providerkit.LinkBucket, Name: "uploads"},
			{Type: providerkit.LinkBucket, Name: "exports"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	plan, err := resources.Releaser(records, fake.NewArtifacts(), &buckets{}).Plan(ctx, providerkit.StackPlan{
		Ref:  ref,
		Kind: providerkit.StackInfra,
		Resources: []providerkit.Resource{
			{Name: "uploads", Type: providerkit.LinkBucket},
			{Name: "invoices", Type: providerkit.LinkBucket},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	rows := rowsOf(plan)
	want := map[string]providerkit.ChangeAction{
		"uploads":  providerkit.ActionKeep,
		"invoices": providerkit.ActionCreate,
		"exports":  providerkit.ActionDelete,
	}
	for name, action := range want {
		if rows[name] != action {
			t.Errorf("%s reads %q, want %q", name, rows[name], action)
		}
	}
	for name, action := range rows {
		if action == providerkit.ActionUpdate {
			t.Errorf("%s reads as an update, and a fan-out with no engine cannot know a resource changed", name)
		}
	}
}

func TestPlanDestroyTakesDownEveryLinkTheStackRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	ref := infraRef()
	if err := providerkit.WriteStack(ctx, records, ref.Class, ref.Project, ref.Name, providerkit.Stack{
		Kind:  providerkit.StackInfra,
		Links: []providerkit.Link{{Type: providerkit.LinkBucket, Name: "uploads"}},
	}); err != nil {
		t.Fatal(err)
	}

	releaser := resources.Releaser(records, fake.NewArtifacts(), &buckets{})
	plan, err := releaser.PlanDestroy(ctx, ref, nil)
	if err != nil {
		t.Fatalf("PlanDestroy() = %v", err)
	}
	if rows := rowsOf(plan); rows["uploads"] != providerkit.ActionDelete {
		t.Errorf("uploads reads %q, want the recorded link shown as going", rows["uploads"])
	}

	absent := ref
	absent.Name = naming.InfraStack("never-provisioned")
	empty, err := releaser.PlanDestroy(ctx, absent, nil)
	if err != nil {
		t.Fatalf("PlanDestroy() of a stack nothing recorded = %v", err)
	}
	if len(empty.Groups) != 0 {
		t.Errorf("PlanDestroy() of a stack nothing recorded returned %+v, want nothing to take down", empty.Groups)
	}
}

type embedded struct {
	*fake.Provider
	fanout providerkit.Releaser
}

func (e embedded) Releases() providerkit.Releaser { return e.fanout }

func (e embedded) Serves() []providerkit.LinkType { return resources.Serves(neon{&buckets{}}) }

func (embedded) Warm(context.Context, []string, providerkit.Reporter) error { return nil }

func TestAWarmerBehindTheFanOutIsStillFoundOnTheRoot(t *testing.T) {
	t.Parallel()

	base := fake.NewProvider(fake.Options{})
	provider := embedded{Provider: base, fanout: resources.Releaser(base.Records(), fake.NewArtifacts(), neon{&buckets{}})}

	if _, warms := providerkit.Provider(provider).(providerkit.Warmer); !warms {
		t.Fatal("the provider's Warmer is not found on the root, so wrapping the release port hid a capability")
	}
	if !slices.Contains(provider.Serves(), providerkit.LinkPostgres) {
		t.Errorf("Serves() = %v, want the Postgres the override advertises", provider.Serves())
	}
}

type withFunctions struct {
	*buckets
	removed []providerkit.Function
}

func (w *withFunctions) ProvisionFunctions(_ context.Context, plan providerkit.StackPlan, _ providerkit.Reporter) ([]providerkit.Function, error) {
	var standing []providerkit.Function
	for _, spec := range plan.App.Functions {
		standing = append(standing, function(plan.Ref, spec.Name))
	}
	return standing, nil
}

func (w *withFunctions) RemoveFunctions(_ context.Context, _ providerkit.StackRef, functions []providerkit.Function, _ providerkit.Reporter) error {
	w.removed = append(w.removed, functions...)
	return nil
}

func appRef() providerkit.StackRef {
	return providerkit.StackRef{
		Project: "shop",
		Class:   providerkit.ClassProduction,
		Name:    naming.AppStack("prod", "web", naming.NewRelease("d1", "f1")),
	}
}

func function(ref providerkit.StackRef, name string) providerkit.Function {
	return providerkit.Function{Name: name, Physical: ref.Name.String() + "-" + name}
}

func recordFunctions(t *testing.T, records providerkit.RecordStore, ref providerkit.StackRef, names ...string) {
	t.Helper()

	stack := providerkit.Stack{Kind: providerkit.StackApp}
	for _, name := range names {
		stack.Functions = append(stack.Functions, function(ref, name))
	}
	if err := providerkit.WriteStack(context.Background(), records, ref.Class, ref.Project, ref.Name, stack); err != nil {
		t.Fatal(err)
	}
}

func TestTheFanOutTakesDownTheFunctionItsPlanShowsGoing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	ref := appRef()
	recordFunctions(t, records, ref, "api", "legacy")

	own := &withFunctions{buckets: &buckets{}}
	releaser := resources.Releaser(records, fake.NewArtifacts(), own)
	plan := providerkit.StackPlan{
		Ref:  ref,
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:       "web",
			Compute:   providerkit.ComputeServerless,
			Functions: []providerkit.FunctionSpec{{Name: "api"}},
		},
	}

	shown, err := releaser.Plan(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if rows := rowsOf(shown); rows["legacy"] != providerkit.ActionDelete {
		t.Fatalf("legacy reads %q, want the function this release stopped declaring shown as going", rows["legacy"])
	}

	if _, err := releaser.Provision(ctx, plan, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "legacy" {
		t.Fatalf("the fan-out took down %v, want the legacy function its plan showed going", own.removed)
	}
}

func TestAReleaseDeclaringNoAppTakesDownTheFunctionsItsPlanShowsGoing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	ref := appRef()
	recordFunctions(t, records, ref, "api")

	own := &withFunctions{buckets: &buckets{}}
	releaser := resources.Releaser(records, fake.NewArtifacts(), own)
	plan := providerkit.StackPlan{Ref: ref, Kind: providerkit.StackInfra}

	shown, err := releaser.Plan(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if rows := rowsOf(shown); rows["api"] != providerkit.ActionDelete {
		t.Fatalf("api reads %q, want the function no release declares shown as going", rows["api"])
	}

	if _, err := releaser.Provision(ctx, plan, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "api" {
		t.Fatalf("the fan-out took down %v, want the function its plan showed going", own.removed)
	}
}

type withContainers struct {
	*buckets
	stood   []providerkit.StackPlan
	removed []providerkit.AppContainer
}

func (w *withContainers) ProvisionContainers(_ context.Context, plan providerkit.StackPlan, _ providerkit.Reporter) ([]providerkit.AppContainer, error) {
	w.stood = append(w.stood, plan)
	return []providerkit.AppContainer{container(plan.Ref, plan.App.App)}, nil
}

func (w *withContainers) RemoveContainers(_ context.Context, _ providerkit.StackRef, containers []providerkit.AppContainer, _ providerkit.Reporter) error {
	w.removed = append(w.removed, containers...)
	return nil
}

func container(ref providerkit.StackRef, name string) providerkit.AppContainer {
	return providerkit.AppContainer{Name: name, Physical: ref.Name.String() + "-" + name}
}

const testImage = "ocel/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func containerApp(app string) *providerkit.AppPlan {
	return &providerkit.AppPlan{
		App:             app,
		Compute:         providerkit.ComputeContainer,
		Image:           testImage,
		HealthCheckPath: "/healthz",
	}
}

func recordContainers(t *testing.T, records providerkit.RecordStore, ref providerkit.StackRef, names ...string) {
	t.Helper()

	stack := providerkit.Stack{Kind: providerkit.StackApp}
	for _, name := range names {
		stack.Containers = append(stack.Containers, container(ref, name))
	}
	if err := providerkit.WriteStack(context.Background(), records, ref.Class, ref.Project, ref.Name, stack); err != nil {
		t.Fatal(err)
	}
}

func TestAContainerAppReachesTheContainerPrimitiveCarryingItsImageAndProbe(t *testing.T) {
	t.Parallel()

	ref := appRef()
	own := &withContainers{buckets: &buckets{}}
	releaser := resources.Releaser(fake.NewRecords(), fake.NewArtifacts(), own)

	result, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:  ref,
		Kind: providerkit.StackApp,
		App:  containerApp("web"),
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.stood) != 1 {
		t.Fatalf("the container primitive was called %d times, want the one app the plan carries", len(own.stood))
	}
	app := own.stood[0].App
	if app.Compute != providerkit.ComputeContainer || app.Image != testImage || app.HealthCheckPath != "/healthz" {
		t.Errorf("the primitive read compute %q, image %q and probe %q, want the flat fields the plan carries", app.Compute, app.Image, app.HealthCheckPath)
	}
	if len(result.Containers) != 1 || result.Containers[0].Name != "web" {
		t.Fatalf("Provision() returned %v, want the container the primitive stood up", result.Containers)
	}
	if len(result.Functions) != 0 {
		t.Errorf("Provision() returned %v, want a container app to stand up no function", result.Functions)
	}
}

func TestAServerlessAppStillReachesFunctions(t *testing.T) {
	t.Parallel()

	own := &withFunctions{buckets: &buckets{}}
	releaser := resources.Releaser(fake.NewRecords(), fake.NewArtifacts(), own)

	result, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:  appRef(),
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:       "web",
			Compute:   providerkit.ComputeServerless,
			Functions: []providerkit.FunctionSpec{{Name: "api"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Functions) != 1 || result.Functions[0].Name != "api" {
		t.Fatalf("Provision() returned %v, want the function the serverless app declares", result.Functions)
	}
	if len(result.Containers) != 0 {
		t.Errorf("Provision() returned %v, want a serverless app to stand up no container", result.Containers)
	}
}

func TestAProviderStandingUpNoContainersRefusesAContainerAppByName(t *testing.T) {
	t.Parallel()

	releaser := resources.Releaser(fake.NewRecords(), fake.NewArtifacts(), &withFunctions{buckets: &buckets{}})

	_, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:  appRef(),
		Kind: providerkit.StackApp,
		App:  containerApp("web"),
	}, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Provision() of a container app on a provider that stands up none = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refusal.Message, resources.AppContainersPrimitive) {
		t.Errorf("the refusal reads %q, want it to name %s, the primitive this provider lacks", refusal.Message, resources.AppContainersPrimitive)
	}
}

func TestAProviderStandingUpNoFunctionsRefusesAServerlessAppByName(t *testing.T) {
	t.Parallel()

	releaser := resources.Releaser(fake.NewRecords(), fake.NewArtifacts(), &withContainers{buckets: &buckets{}})

	_, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:  appRef(),
		Kind: providerkit.StackApp,
		App:  &providerkit.AppPlan{App: "web", Compute: providerkit.ComputeServerless},
	}, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Provision() of a serverless app on a provider that stands up none = %v, want an invalid refusal", err)
	}
	if !strings.Contains(refusal.Message, resources.FunctionsPrimitive) {
		t.Errorf("the refusal reads %q, want it to name %s, the primitive this provider lacks", refusal.Message, resources.FunctionsPrimitive)
	}
}

func TestAnAppNamingNoComputeIsRefusedRatherThanAssumedServerless(t *testing.T) {
	t.Parallel()

	releaser := resources.Releaser(fake.NewRecords(), fake.NewArtifacts(), &withFunctions{buckets: &buckets{}})

	_, err := releaser.Provision(context.Background(), providerkit.StackPlan{
		Ref:  appRef(),
		Kind: providerkit.StackApp,
		App:  &providerkit.AppPlan{App: "web"},
	}, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Provision() of an app naming no compute = %v, want an invalid refusal", err)
	}
}

func TestTheFanOutTakesDownTheContainerItsPlanShowsGoing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	ref := appRef()
	recordContainers(t, records, ref, "web", "legacy")

	own := &withContainers{buckets: &buckets{}}
	releaser := resources.Releaser(records, fake.NewArtifacts(), own)
	plan := providerkit.StackPlan{Ref: ref, Kind: providerkit.StackApp, App: containerApp("web")}

	shown, err := releaser.Plan(ctx, plan, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	rows := rowsOf(shown)
	if rows["legacy"] != providerkit.ActionDelete {
		t.Fatalf("legacy reads %q, want the container this release stopped declaring shown as going", rows["legacy"])
	}
	if rows["web"] != providerkit.ActionKeep {
		t.Errorf("web reads %q, want the container this release still declares kept", rows["web"])
	}

	if _, err := releaser.Provision(ctx, plan, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "legacy" {
		t.Fatalf("the fan-out took down %v, want the legacy container its plan showed going", own.removed)
	}
}

func TestAProviderStandingUpNoContainersRefusesToOrphanTheOnesItRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	ref := appRef()
	recordContainers(t, records, ref, "legacy")

	releaser := resources.Releaser(records, fake.NewArtifacts(), &withFunctions{buckets: &buckets{}})

	_, err := releaser.Provision(ctx, providerkit.StackPlan{
		Ref:  ref,
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:       "web",
			Compute:   providerkit.ComputeServerless,
			Functions: []providerkit.FunctionSpec{{Name: "api"}},
		},
	}, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Message, resources.AppContainersPrimitive) {
		t.Fatalf("Provision() over a recorded container nothing can take down = %v, want a refusal naming %s", err, resources.AppContainersPrimitive)
	}
}

func TestDestroyTakesDownEveryContainerTheStackRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	ref := appRef()
	recordContainers(t, records, ref, "web")

	own := &withContainers{buckets: &buckets{}}
	releaser := resources.Releaser(records, fake.NewArtifacts(), own)

	shown, err := releaser.PlanDestroy(ctx, ref, nil)
	if err != nil {
		t.Fatalf("PlanDestroy() = %v", err)
	}
	if rows := rowsOf(shown); rows["web"] != providerkit.ActionDelete {
		t.Errorf("web reads %q, want the recorded container shown as going", rows["web"])
	}
	if err := releaser.Destroy(ctx, ref, nil); err != nil {
		t.Fatalf("Destroy() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "web" {
		t.Fatalf("Destroy() took down %v, want the container the stack recorded", own.removed)
	}
}

func TestDestroyRefusesByNameWhenNothingCanTakeTheRecordedContainerDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	ref := appRef()
	recordContainers(t, records, ref, "web")

	err := resources.Releaser(records, fake.NewArtifacts(), &withFunctions{buckets: &buckets{}}).Destroy(ctx, ref, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Message, resources.AppContainersPrimitive) {
		t.Fatalf("Destroy() of a recorded container nothing stands up = %v, want a refusal naming %s", err, resources.AppContainersPrimitive)
	}
}

func TestAnAppMovingToAComputeThisProviderLacksIsRefusedBeforeItsFunctionsAreTakenDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	ref := appRef()
	recordFunctions(t, records, ref, "api")

	own := &withFunctions{buckets: &buckets{}}

	_, err := resources.Releaser(records, fake.NewArtifacts(), own).Provision(ctx, providerkit.StackPlan{
		Ref:  ref,
		Kind: providerkit.StackApp,
		App:  containerApp("web"),
	}, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Message, resources.AppContainersPrimitive) {
		t.Fatalf("Provision() of a container app on a provider that stands up none = %v, want a refusal naming %s", err, resources.AppContainersPrimitive)
	}
	if len(own.removed) != 0 {
		t.Fatalf("the fan-out took down %v on a release it then refused, leaving the app down with nothing standing in its place", own.removed)
	}
}

func TestAnAppNamingNoComputeIsRefusedBeforeItsContainerIsTakenDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	records := fake.NewRecords()
	ref := appRef()
	recordContainers(t, records, ref, "web")

	own := &withContainers{buckets: &buckets{}}

	_, err := resources.Releaser(records, fake.NewArtifacts(), own).Provision(ctx, providerkit.StackPlan{
		Ref:  ref,
		Kind: providerkit.StackApp,
		App:  &providerkit.AppPlan{App: "web"},
	}, nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Provision() of an app naming no compute = %v, want an invalid refusal", err)
	}
	if len(own.removed) != 0 {
		t.Fatalf("the fan-out took down %v on a release it then refused, leaving the app down with nothing standing in its place", own.removed)
	}
}
