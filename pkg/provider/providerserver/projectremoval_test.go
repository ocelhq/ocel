package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func projectRequest() *contractv1.ProjectRequest {
	return &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}
}

func deployedProject(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	builtProject(t)
	client, vendor := deployServed(t)
	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	return client, vendor
}

func kinds(plan *planv1.ChangePlan) []string {
	var out []string
	for _, item := range plan.GetGroups() {
		out = append(out, item.GetKind())
	}
	return out
}

func TestPlanRemoveProjectNamesEveryStackTheDeployProvisioned(t *testing.T) {
	client, _ := deployedProject(t)

	plan, err := client.PlanRemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	planned := kinds(plan)
	for _, kind := range []string{provider.StackGroupKind, edge.EdgeGroupKind, "variable values", "stored objects"} {
		if !slices.Contains(planned, kind) {
			t.Errorf("the plan names %v, want a %q group among them", planned, kind)
		}
	}
	if plan.GetSubject() != "shop" {
		t.Errorf("the plan's subject is %q, want the project it removes", plan.GetSubject())
	}
	for _, group := range plan.GetGroups() {
		if group.GetName() == "" {
			t.Errorf("the plan contains %+v, and the CLI cannot render a nameless group", group)
		}
		if group.GetKind() == provider.StackGroupKind && !strings.HasPrefix(group.GetName(), "fake/") {
			t.Errorf("the plan contains %+v, want every stack named under the vendor that hosts it", group)
		}
		for _, change := range group.GetChanges() {
			if change.GetKind() == "" || change.GetName() == "" {
				t.Errorf("the plan contains row %+v, and a row renders as a name in a type column", change)
			}
		}
	}
}

func TestRemovingAProjectNoEdgeStackServedRemovesItAsAUserOfTheBootstrapParts(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	var removed []string
	vendor.FakeBootstrap().RemovingPartUser(func(_ context.Context, tier environment.Tier, project string, _ progress.Log) error {
		removed = append(removed, string(tier)+"/"+project)
		return nil
	})

	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}
	if !slices.Equal(removed, []string{"production/shop"}) {
		t.Errorf("removing shop removed part users %v, want shop removed from the parts its first deploy claimed before it failed", removed)
	}
}

func TestPlanRemoveProjectOfAProjectNothingDeployedNamesNoStack(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	plan, err := client.PlanRemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	if slices.Contains(kinds(plan), provider.StackGroupKind) {
		t.Errorf("the plan names %v for a project that never deployed", kinds(plan))
	}
}

func protectedDatabaseTeardown(ref provider.StackRef) provider.Plan {
	if !ref.Name.IsInfra() {
		return provider.Plan{}
	}
	return provider.Plan{Groups: []provider.ChangeGroup{{
		Kind:   provider.StackGroupKind,
		Name:   ref.Name.String(),
		Action: provider.ActionDelete,
		Changes: []provider.Change{
			{Kind: "database cluster", Name: "db", Action: provider.ActionDisableThenDelete, Reason: "its deletion protection is disabled first", Slow: true},
			{Kind: "bucket", Name: "uploads", Action: provider.ActionDelete},
		},
	}}}
}

func TestPlanRemoveProjectShowsAProductionDatabaseDisabledBeforeItIsDeleted(t *testing.T) {
	client, vendor := deployedProject(t)
	vendor.FakeStacks().PlanningDestroy(protectedDatabaseTeardown)

	plan, err := client.PlanRemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}

	var rows []string
	for _, group := range plan.GetGroups() {
		if group.GetKind() != provider.StackGroupKind {
			continue
		}
		for _, change := range group.GetChanges() {
			rows = append(rows, change.GetName()+" "+change.GetAction().String())
		}
	}
	for _, want := range []string{"db ACTION_DISABLE_THEN_DELETE", "uploads ACTION_DELETE"} {
		if !slices.Contains(rows, want) {
			t.Errorf("the plan's stack rows are %v, want %q: a destroy that disables a deletion protection is consented to by the plan that shows it", rows, want)
		}
	}
}

func TestARemovalRunsAConsentedProductionPlanThatShowsADatabaseDisabledBeforeItIsDeleted(t *testing.T) {
	ctx := context.Background()
	client, vendor := deployedProject(t)
	vendor.FakeStacks().PlanningDestroy(protectedDatabaseTeardown)

	consented, err := client.PlanRemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	req := projectRequest()
	req.Consented = consented
	if result := removeProject(t, client, req); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the plan the human saw to run", result.GetError())
	}
}

func TestPlanRemoveProjectOfPreviewsNamesTheirStacksWithoutPlanningEachTeardown(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	if result, _ := deploy(t, client, previewRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	var mu sync.Mutex
	var planned []string
	vendor.FakeStacks().PlanningDestroy(func(ref provider.StackRef) provider.Plan {
		mu.Lock()
		defer mu.Unlock()
		planned = append(planned, ref.Name.String())
		return provider.Plan{}
	})

	plan, err := client.PlanRemoveProject(context.Background(), &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
	})
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	if !slices.Contains(kinds(plan), provider.StackGroupKind) {
		t.Fatalf("the plan names %v, want the preview's stacks among them", kinds(plan))
	}
	if len(planned) > 0 {
		t.Errorf("the plan previewed the teardown of %v, want a preview, which protects nothing from deletion, planned from its records alone", planned)
	}
}

func TestRemoveProjectDestroysEveryStackAndForgetsTheProject(t *testing.T) {
	client, vendor := deployedProject(t)

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}

	entries, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the project still records %v, want every stack forgotten", entries)
	}
}

func removeProject(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ProjectRequest) *progressv1.OperationResult {
	t.Helper()
	stream, err := client.RemoveProject(context.Background(), req)
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRemoveProjectIsRefusedWhileADeployHoldsProduction(t *testing.T) {
	client, vendor := deployedProject(t)
	takeLeaseOver(t, vendor.KeyValues(), otherEnvironmentLease)

	result := removeProject(t, client, projectRequest())

	if result.GetSuccess() || !strings.Contains(result.GetError(), "a deploy to prod is running: remove it again once it ends") {
		t.Fatalf("RemoveProject() = %q, want it refused while a deploy holds production", result.GetError())
	}
	if entries, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop"); err != nil || len(entries) == 0 {
		t.Errorf("the refused removal left %d stacks recorded (%v), want every stack kept", len(entries), err)
	}
}

func TestRemovingEveryPreviewIsRefusedWhileAFirstDeployToOneHoldsItsLease(t *testing.T) {
	client, vendor := deployedProject(t)
	recordLease(t, vendor.KeyValues(), environment.TierPreview, "pr-9", otherEnvironmentLease)

	result := removeProject(t, client, &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
	})

	if result.GetSuccess() || !strings.Contains(result.GetError(), "a deploy to pr-9 is running") {
		t.Fatalf("RemoveProject() of every preview = %q, want it refused while a deploy holds pr-9, though pr-9 records no stack yet", result.GetError())
	}
}

func TestRemoveProjectHoldsProductionWhileItRemovesItAndLeavesNoLeaseBehind(t *testing.T) {
	client, vendor := deployedProject(t)
	var during error
	relayPlane(vendor).BeforeNextPointerRemoval(func() {
		during = takeLeaseAsDeploy(vendor.KeyValues(), environment.TierProduction, stackrecords.ProductionEnv)
	})

	if result := removeProject(t, client, projectRequest()); !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, want the project removed", result.GetError())
	}

	if during == nil || !strings.Contains(during.Error(), "a removal of shop in production is running") {
		t.Errorf("a deploy taking production while it was removed = %v, want it refused because the removal holds production", during)
	}
	if isLeaseHeld(t, vendor.KeyValues(), environment.TierProduction, stackrecords.ProductionEnv) {
		t.Error("production still records a lease after the project was removed, want nothing left behind")
	}
}

func TestRemoveProjectErasesItsLedger(t *testing.T) {
	client, vendor := deployedProject(t)
	partition := ledger.Partition(environment.TierProduction, "shop")
	if kept, err := vendor.KeyValues().List(context.Background(), partition); err != nil || len(kept) == 0 {
		t.Fatalf("the deploy left %d ledger entries (%v), and a removal that erases none proves nothing", len(kept), err)
	}

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("RemoveProject() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v, want the project removed", result.GetError(), err)
	}

	kept, err := vendor.KeyValues().List(context.Background(), partition)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 0 {
		t.Errorf("the ledger keeps %d entries after the project was removed, want none: a destroyed project leaves no bytes behind", len(kept))
	}
}

func TestRemoveProjectPurgesTheValuesAndObjectsItsReleasesWrote(t *testing.T) {
	client, vendor := deployedProject(t)
	ctx := context.Background()

	specs := vendor.FakeStacks().Provisioned()
	ref := vendor.FakeStacks().Shipped(specs[1].Ref)[0].Ref

	stream, err := client.RemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	if opened, err := vendor.Artifacts().Open(ctx, ref); err == nil {
		opened.Close()
		t.Errorf("the artifact at %s survived the removal, want the project's whole prefix gone", ref.Key)
	}

	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	names, err := store.PublishedNames(ctx, variablestore.Scope{Project: "shop", Tier: environment.TierProduction}, stackrecords.ProductionEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Errorf("the removal left bindings %v published, want the project's values purged", names)
	}
}

func TestRemoveProjectLeavesNothingInTheProjectsValuesOrReferencesPartitions(t *testing.T) {
	client, vendor := deployedProject(t)
	ctx := context.Background()

	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	shop := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	region := variablestore.Coordinate{Cell: variablestore.Cell{Folder: "/", Key: "REGION"}}
	if _, err := store.Set(ctx, shop, region, "eu", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetReference(ctx, variablestore.Scope{Project: "blog", Tier: environment.TierProduction}, region,
		variablestore.Target{Project: "shop", Cell: region.Cell}); err != nil {
		t.Fatal(err)
	}
	partitions := map[string]records.Partition{
		"values":     variablestore.ValuesPartition(shop),
		"references": variablestore.ReferencesPartition(shop),
	}
	for what, partition := range partitions {
		if kept, err := vendor.KeyValues().List(ctx, partition); err != nil || len(kept) == 0 {
			t.Fatalf("the project's %s partition holds %d entries (%v) before the removal, and a removal that purges none proves nothing", what, len(kept), err)
		}
	}

	stream, err := client.RemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	for what, partition := range partitions {
		kept, err := vendor.KeyValues().List(ctx, partition)
		if err != nil {
			t.Fatal(err)
		}
		if len(kept) != 0 {
			t.Errorf("the project's %s partition keeps %d entries after the removal, want none: a destroyed project leaves no bytes behind", what, len(kept))
		}
	}
}

func TestRemoveProjectOnThePreviewTierForgetsEveryPreviewsEnvironmentRecord(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	if result, _ := deploy(t, client, previewRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	ctx := context.Background()
	partition := stackrecords.EnvironmentsPartition(environment.TierPreview, "shop")
	if kept, err := vendor.KeyValues().List(ctx, partition); err != nil || len(kept) == 0 {
		t.Fatalf("the preview deploy recorded %d environments (%v), and a removal that forgets none proves nothing", len(kept), err)
	}

	stream, err := client.RemoveProject(ctx, &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	kept, err := vendor.KeyValues().List(ctx, partition)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range kept {
		t.Errorf("%s is still recorded after the project's previews were removed: a destroyed project leaves no bytes behind, and a later preview of that name would read its lifecycle and alias", entry.Key)
	}
}

func TestRemoveProjectRetiresTheISRPrefixOfEveryReleaseBeforeSweepingTheProject(t *testing.T) {
	client, vendor := deployedProject(t)

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	var want []string
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App == nil {
			continue
		}
		coordinate := naming.Coordinate{Project: "shop", Env: spec.Ref.Name.Env, App: spec.Ref.Name.App, Release: spec.Ref.Name.Release}
		want = append(want, "remove-prefix "+coordinate.ISRPrefix())
	}
	if len(want) == 0 {
		t.Fatal("the deploy provisioned no app stack to remove")
	}
	inOrder(t, vendor.Journal(), append(want, "remove-prefix "+naming.Coordinate{Project: "shop", Env: stackrecords.ProductionEnv}.StoragePrefix())...)
}

func TestRemoveProjectForgetsItsEnvSourceAndHowItsSyncsWent(t *testing.T) {
	client, vendor := deployedProject(t)
	ctx := context.Background()
	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	registration := envsource.Registration{Project: "shop", Descriptor: execDescriptor(t), Folders: []string{""}}
	if _, err := envsource.Register(ctx, store, environment.TierProduction, registration); err != nil {
		t.Fatal(err)
	}
	sync := &envsource.Sync{Store: store, Tier: environment.TierProduction}
	if _, err := sync.CopyProjectFrom(ctx, registration, envsource.NewFixed("exec", nil)); err != nil {
		t.Fatal(err)
	}

	stream, err := client.RemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	if _, registered, err := envsource.Registered(ctx, store.KeyValues, environment.TierProduction, "shop"); err != nil || registered {
		t.Errorf("Registered() after the removal = %v, %v, want a scheduled sync to stop reading for a project that is gone", registered, err)
	}
	if status, err := envsource.StatusOf(ctx, store, environment.TierProduction, registration); err != nil || !status.LastAttemptAt.IsZero() {
		t.Errorf("StatusOf() after the removal = %+v, %v, want the status record removed with the project", status, err)
	}
}

func TestRemoveProjectForgetsThePartsItWantedAndNoOtherProjectsWants(t *testing.T) {
	client, vendor := deployedProject(t)
	ctx := context.Background()
	recordWant(t, vendor, fake.PartImages, "shop")
	recordWant(t, vendor, fake.PartImages, "blog")

	stream, err := client.RemoveProject(ctx, projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}

	if wants := wantedParts(t, vendor, environment.TierProduction); !slices.Equal(wants[fake.PartImages], []string{"blog"}) {
		t.Errorf("wants after the removal = %v, want blog's alone: shop is gone", wants)
	}
}

func TestRemoveProjectRefusesACallNamingNoProject(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	if _, err := client.PlanRemoveProject(context.Background(), &contractv1.ProjectRequest{
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	}); err == nil {
		t.Fatal("PlanRemoveProject() with no slug succeeded, want it refused")
	}
}
