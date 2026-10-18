package resources_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type buckets struct {
	removed []provider.Binding
}

func (b *buckets) ProvisionBucket(_ context.Context, in resources.ProvisionRequest, _ progress.Log) (provider.Binding, error) {
	return provider.Binding{
		Type:       provider.BindingBucket,
		Name:       in.Resource.Name,
		Properties: map[string]string{provider.PropertyBucket: in.Ref.Project + "-" + in.Resource.Name},
	}, nil
}

func (b *buckets) RemoveResource(_ context.Context, _ provider.StackRef, binding provider.Binding, _ progress.Log) error {
	b.removed = append(b.removed, binding)
	return nil
}

func (b *buckets) hooks() resources.Hooks {
	return resources.Hooks{Bucket: &resources.ResourceHooks{Plan: planFromRecord, Provision: b.ProvisionBucket, Remove: b.RemoveResource}}
}

func planFromRecord(_ context.Context, _ resources.ProvisionRequest, recorded *provider.Binding) (provider.ChangeAction, string, error) {
	if recorded == nil {
		return provider.ActionCreate, "", nil
	}
	return provider.ActionKeep, provider.ReasonCurrent, nil
}

func provisioning(provision func(context.Context, resources.ProvisionRequest, progress.Log) (provider.Binding, error)) *resources.ResourceHooks {
	return &resources.ResourceHooks{Plan: planFromRecord, Provision: provision, Remove: (&buckets{}).RemoveResource}
}

type neon struct{ *buckets }

func (n neon) hooks() resources.Hooks {
	hooks := n.buckets.hooks()
	hooks.Postgres = &resources.ResourceHooks{Plan: planFromRecord, Provision: n.ProvisionPostgres, Remove: n.RemoveResource}
	return hooks
}

func (neon) ProvisionPostgres(_ context.Context, in resources.ProvisionRequest, _ progress.Log) (provider.Binding, error) {
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
	return resources.Hooks{Postgres: provisioning(h.ProvisionPostgres)}
}

func (halfBinding) ProvisionPostgres(_ context.Context, in resources.ProvisionRequest, _ progress.Log) (provider.Binding, error) {
	return provider.Binding{
		Type:       provider.BindingPostgres,
		Name:       in.Resource.Name,
		Properties: map[string]string{provider.PropertyHost: "db.invalid"},
	}, nil
}

func infraRef() provider.StackRef {
	return provider.StackRef{
		Project: "shop",
		Tier:    environment.TierProduction,
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
	stores := resources.Hooks{KV: provisioning((&buckets{}).ProvisionBucket)}
	if served := resources.ServedBindingTypes(stores); !slices.Equal(served, []provider.BindingType{provider.BindingKV}) {
		t.Fatalf("ServedBindingTypes() = %v, want the kv stores the hooks provision", served)
	}
	queues := resources.Hooks{Topic: provisioning((&buckets{}).ProvisionBucket)}
	if served := resources.ServedBindingTypes(queues); !slices.Equal(served, []provider.BindingType{provider.BindingTopic, provider.BindingTask}) {
		t.Fatalf("ServedBindingTypes() = %v, want the topics and the tasks one hook provisions: a task is a topic with one consumer", served)
	}
	channels := resources.Hooks{Realtime: provisioning((&buckets{}).ProvisionBucket)}
	if served := resources.ServedBindingTypes(channels); !slices.Equal(served, []provider.BindingType{provider.BindingRealtime}) {
		t.Fatalf("ServedBindingTypes() = %v, want the realtime stores the hooks provision", served)
	}
}

func TestHookStacksFanEachResourceOutToItsPrimitive(t *testing.T) {
	t.Parallel()

	store := fake.NewKeyValues()
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

func TestHookStacksProvisionNothingForAResourceOcelJSONBindsToAPublishedRecord(t *testing.T) {
	t.Parallel()

	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), neon{&buckets{}}.hooks())

	result, err := stacks.Provision(context.Background(), provider.StackSpec{
		Ref:  infraRef(),
		Kind: provider.StackInfra,
		Resources: []provider.Resource{
			{Name: "orders", Type: provider.BindingPostgres, Binding: "orders"},
			{Name: "uploads", Type: provider.BindingBucket},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Bindings) != 1 || result.Bindings[0].Name != "uploads" {
		t.Fatalf("Provision() returned %v, want only the uploads bucket: a publisher runs the orders database ocel.json binds", result.Bindings)
	}
}

func TestHookStacksRefuseAPrimitiveNothingServes(t *testing.T) {
	t.Parallel()

	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), (&buckets{}).hooks())

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

	_, err := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), (&buckets{}).hooks()).Plan(context.Background(), provider.StackSpec{
		Ref:       infraRef(),
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "orders", Type: provider.BindingPostgres}},
	}, nil)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("Plan() of a primitive nothing serves = %v, want the refusal the provision would give", err)
	}
}

func TestHookStacksRefuseABindingMissingAPropertyItsTypePromises(t *testing.T) {
	t.Parallel()

	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), halfBinding{}.hooks())

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

func TestHookStacksRemoveAResourceTheSpecNoLongerDeclares(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	own := &buckets{}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
	ref := infraRef()

	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind: provider.StackInfra,
		Bindings: []provider.Binding{
			{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{provider.PropertyBucket: "shop-uploads"}},
			{Type: provider.BindingBucket, Name: "exports", Properties: map[string]string{provider.PropertyBucket: "shop-exports"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	progress := &fake.Log{}
	if _, err := stacks.Provision(ctx, provider.StackSpec{
		Ref:       ref,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "uploads", Type: provider.BindingBucket}},
	}, progress); err != nil {
		t.Fatalf("Provision() = %v", err)
	}

	if len(own.removed) != 1 || own.removed[0].Name != "exports" {
		t.Fatalf("the fan-out removed %v, want only the export bucket this spec stopped declaring", own.removed)
	}
	saysRemoving(t, progress, "bucket exports")
}

func TestHookStacksRemoveAResourceTheyProvisionedOnceOcelJSONBindsItToAPublishedRecord(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	own := &buckets{}
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
	ref := infraRef()

	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind:     provider.StackInfra,
		Bindings: []provider.Binding{{Type: provider.BindingBucket, Name: "uploads", Properties: map[string]string{provider.PropertyBucket: "shop-uploads"}}},
	}); err != nil {
		t.Fatal(err)
	}
	spec := provider.StackSpec{
		Ref:       ref,
		Kind:      provider.StackInfra,
		Resources: []provider.Resource{{Name: "uploads", Type: provider.BindingBucket, Binding: "uploads"}},
	}

	plan, err := stacks.Plan(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if action := rowsOf(plan)["uploads"]; action != provider.ActionDelete {
		t.Errorf("uploads reads %q, want it deleted: the release that follows takes down the bucket a publisher now runs in its place", action)
	}

	progress := &fake.Log{}
	result, err := stacks.Provision(ctx, spec, progress)
	if err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(result.Bindings) != 0 {
		t.Errorf("Provision() returned %v, want nothing: a publisher runs the bucket ocel.json binds", result.Bindings)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "uploads" {
		t.Fatalf("the fan-out removed %v, want the uploads bucket it provisioned, which no record would own once this release drops it", own.removed)
	}
	saysRemovingBecause(t, progress, "bucket uploads", "ocel.json binds it to a published record")
}

func saysRemoving(t *testing.T, progress *fake.Log, what string) {
	t.Helper()
	saysRemovingBecause(t, progress, what, "this release no longer declares it")
}

func saysRemovingBecause(t *testing.T, progress *fake.Log, what, because string) {
	t.Helper()
	want := "INFO Removing " + what + ": " + because
	if lines := progress.Lines(); !slices.Contains(lines, want) {
		t.Errorf("the fan-out said %q, want %q: a sentence is said rather than shown as tool output, and names what kind of thing goes", lines, want)
	}
}

func TestDestroyOfAStackNothingRecordedIsANoOp(t *testing.T) {
	t.Parallel()

	own := &buckets{}
	stacks := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), own.hooks())

	if err := stacks.Destroy(context.Background(), provider.StackDestroy{Ref: infraRef()}, nil); err != nil {
		t.Fatalf("Destroy() of a stack nothing recorded = %v, want nil", err)
	}
	if len(own.removed) != 0 {
		t.Errorf("Destroy() removed %v for a stack that was never provisioned", own.removed)
	}
}

func TestDestroyTakesDownEveryBindingTheStackRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	own := &buckets{}
	ref := infraRef()

	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
		Kind:     provider.StackInfra,
		Bindings: []provider.Binding{{Type: provider.BindingBucket, Name: "uploads"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks()).Destroy(ctx, provider.StackDestroy{Ref: ref}, nil); err != nil {
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

	plan, err := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), neon{&buckets{}}.hooks()).Plan(context.Background(), provider.StackSpec{
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

func TestPlanShowsNothingProvisionedForAResourceOcelJSONBindsToAPublishedRecord(t *testing.T) {
	t.Parallel()

	plan, err := resources.NewHookStacks(fake.NewKeyValues(), fake.NewArtifacts(), neon{&buckets{}}.hooks()).Plan(context.Background(), provider.StackSpec{
		Ref:  infraRef(),
		Kind: provider.StackInfra,
		Resources: []provider.Resource{
			{Name: "orders", Type: provider.BindingPostgres, Binding: "orders"},
			{Name: "uploads", Type: provider.BindingBucket},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	rows := rowsOf(plan)
	if action, shown := rows["orders"]; shown {
		t.Errorf("orders reads %q, want no row: a publisher runs the database ocel.json binds, so this release provisions nothing for it", action)
	}
	if rows["uploads"] != provider.ActionCreate {
		t.Errorf("uploads reads %q, want it created", rows["uploads"])
	}
}

func TestPlanKeepsWhatExistsAndDeletesWhatThePlanDropped(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := infraRef()
	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
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
	store := fake.NewKeyValues()
	ref := infraRef()
	if err := stackrecords.Write(ctx, store, ref.Tier, ref.Project, ref.Name, stackrecords.Stack{
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
	hooks.Functions = &resources.FunctionHooks{
		Provision: w.ProvisionFunctions, Remove: w.RemoveFunctions,
	}
	return hooks
}

func (w *withFunctions) ProvisionFunctions(_ context.Context, spec provider.StackSpec, _ progress.Log) ([]provider.Function, error) {
	var functions []provider.Function
	for _, fn := range spec.App.Functions {
		functions = append(functions, function(spec.Ref, fn.Name))
	}
	return functions, nil
}

func (w *withFunctions) RemoveFunctions(_ context.Context, _ provider.StackRef, functions []provider.Function, _ provider.ImageStore, _ progress.Log) error {
	w.removed = append(w.removed, functions...)
	return nil
}

func appRef() provider.StackRef {
	return provider.StackRef{
		Project: "shop",
		Tier:    environment.TierProduction,
		Name:    naming.AppStack("prod", "web", naming.NewReleaseToken("d1", "f1")),
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
	if err := stackrecords.Write(context.Background(), store, ref.Tier, ref.Project, ref.Name, stack); err != nil {
		t.Fatal(err)
	}
}

func TestTheFanOutTakesDownTheFunctionItsPlanShowsGoing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
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

	progress := &fake.Log{}
	if _, err := stacks.Provision(ctx, spec, progress); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != "legacy" {
		t.Fatalf("the fan-out took down %v, want the legacy function its plan showed going", own.removed)
	}
	saysRemoving(t, progress, "function legacy")
}

func TestAReleaseDeclaringNoAppTakesDownTheFunctionsItsPlanShowsGoing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
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

func TestAWorkerAnAppStillHostsIsKeptAndOneItDroppedIsTakenDown(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := appRef()
	recordFunctions(t, store, ref, "web", resources.WorkerName("media"), resources.WorkerName("legacy"))

	own := &withFunctions{buckets: &buckets{}}
	hooks := own.hooks()
	hooks.RecordsWorkers = true
	stacks := resources.NewHookStacks(store, fake.NewArtifacts(), hooks)
	spec := provider.StackSpec{
		Ref:  ref,
		Kind: provider.StackApp,
		App: &provider.AppSpec{
			App:       "web",
			Compute:   provider.ComputeServerless,
			Functions: []provider.FunctionSpec{{Name: "web"}},
			Workers:   []provider.WorkerSpec{{Name: "media"}},
		},
	}

	shown, err := stacks.Plan(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	rows := rowsOf(shown)
	if rows[resources.WorkerName("media")] != provider.ActionKeep || rows[resources.WorkerName("legacy")] != provider.ActionDelete {
		t.Fatalf("Plan() shows %v, want the media worker kept and the legacy worker the app no longer hosts going", rows)
	}
	if _, err := stacks.Provision(ctx, spec, nil); err != nil {
		t.Fatalf("Provision() = %v", err)
	}
	if len(own.removed) != 1 || own.removed[0].Name != resources.WorkerName("legacy") {
		t.Fatalf("the fan-out took down %v, want the legacy worker alone", own.removed)
	}
}

func TestAVendorThatRecordsNoWorkersPlansNoWorkerRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, compute := range []provider.Compute{provider.ComputeServerless, provider.ComputeContainer} {
		store := fake.NewKeyValues()
		ref := appRef()
		recordFunctions(t, store, ref, "web")
		own := &withFunctions{buckets: &buckets{}}
		stacks := resources.NewHookStacks(store, fake.NewArtifacts(), own.hooks())
		spec := provider.StackSpec{
			Ref:  ref,
			Kind: provider.StackApp,
			App: &provider.AppSpec{
				App:       "web",
				Compute:   compute,
				Functions: []provider.FunctionSpec{{Name: "web"}},
				Workers:   []provider.WorkerSpec{{Name: "media"}},
			},
		}

		shown, err := stacks.Plan(ctx, spec, nil)
		if err != nil {
			t.Fatalf("Plan() = %v", err)
		}
		if action, shownWorker := rowsOf(shown)[resources.WorkerName("media")]; shownWorker {
			t.Errorf("a %s app's plan shows its worker %s, want no worker row from a vendor that records its workers elsewhere", compute, action)
		}
	}
}

func TestAWorkerIsNamedApartFromEveryFunction(t *testing.T) {
	t.Parallel()

	if name := resources.WorkerName("media"); name == "media" || !strings.Contains(name, "media") {
		t.Errorf("WorkerName(media) = %q, want a name holding the worker's and no function's", name)
	}
}

func TestAWorkersNameReadsBackAsTheWorker(t *testing.T) {
	t.Parallel()

	if worker, isWorker := resources.WorkerOf(resources.WorkerName("media")); !isWorker || worker != "media" {
		t.Errorf("WorkerOf(WorkerName(media)) = %q, %v, want media, true", worker, isWorker)
	}
	if worker, isWorker := resources.WorkerOf("web-server"); isWorker {
		t.Errorf("WorkerOf(web-server) = %q, true, want a function's name to be no worker's", worker)
	}
}
