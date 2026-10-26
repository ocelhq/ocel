package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func recordWant(t *testing.T, vendor *fake.Provider, part, project string) {
	t.Helper()
	if err := stackrecords.RecordBootstrapWant(context.Background(), vendor.KeyValues(), environment.TierProduction, part, project); err != nil {
		t.Fatal(err)
	}
}

func TestABootstrapNamingNoPartAddsEveryPartARecordedProjectWants(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.PartCache, "shop")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	applied := vendor.FakeBootstrap().Applied()
	if last := applied[len(applied)-1]; !slices.Contains(last.Parts, fake.PartCache) {
		t.Errorf("Apply() parts = %v, want %s, which shop wants", last.Parts, fake.PartCache)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(context.Background(), vendor.KeyValues(), environment.TierProduction, fake.PartCache)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" {
		t.Errorf("ReadBootstrapPart(%s) = %+v, %v, %v, want it ready and added for shop", fake.PartCache, recorded, present, err)
	}
}

func TestAPartABootstrapAddsIsAddedByTheFirstProjectThatWantedIt(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.PartCache, "shop")
	time.Sleep(time.Millisecond)
	recordWant(t, vendor, fake.PartCache, "blog")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	recorded, _, err := stackrecords.ReadBootstrapPart(context.Background(), vendor.KeyValues(), environment.TierProduction, fake.PartCache)
	if err != nil || recorded.AddedBy != "shop" {
		t.Errorf("ReadBootstrapPart(%s) = %+v, %v, want it added by shop, which wanted it before blog", fake.PartCache, recorded, err)
	}
}

func TestAPartABootstrapAddsForAProjectReadsAsAddedByTheBootstrapForThatProject(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.PartCache, "shop")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	if part := describedParts(t, client)[fake.PartCache]; part.GetAddedWith() != "ocel bootstrap" || part.GetAddedBy() != "shop" {
		t.Errorf("%s added with %q for %q, want `ocel bootstrap` for shop: no deploy of shop added it", fake.PartCache, part.GetAddedWith(), part.GetAddedBy())
	}
}

func TestABootstrapNamingNoPartKeepsAndMovesForwardEveryPartItHolds(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.PartCache, "shop")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	applied := vendor.FakeBootstrap().Applied()
	if last := applied[len(applied)-1]; !slices.Contains(last.Parts, fake.PartCache) {
		t.Errorf("Apply() parts = %v, want %s kept: a bootstrap only ever builds up", last.Parts, fake.PartCache)
	}
}

func TestAPartABootstrapAddedIsNoLongerWanted(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.PartCache, "shop")
	recordWant(t, vendor, fake.PartCache, "blog")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	if wants := wantedParts(t, vendor, environment.TierProduction); len(wants) != 0 {
		t.Errorf("wants = %v, want none: the bootstrap added every part they named", wants)
	}
}

func TestADryBootstrapPlansTheWantedPartsAndRecordsNothing(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.PartCache, "shop")

	plan := streamedPlan(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct, Dry: true})

	if !slices.ContainsFunc(plan.GetGroups(), func(group *planv1.ChangeGroup) bool { return group.GetPart() == fake.PartCache }) {
		t.Errorf("plan groups = %v, want one adding %s", plan.GetGroups(), fake.PartCache)
	}
	if wants := wantedParts(t, vendor, environment.TierProduction); !slices.Equal(wants[fake.PartCache], []string{"shop"}) {
		t.Errorf("wants = %v, want shop's want of %s kept by a dry run", wants, fake.PartCache)
	}
}

func TestABootstrapRemovingAPartAProjectWantsRemovesItAndAddsNothing(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	bootstrapped(t, vendor, environment.TierProduction, fake.PartCache)
	recordWant(t, vendor, fake.PartCache, "shop")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct, Remove: []string{fake.PartCache}, Force: true})

	applied := vendor.FakeBootstrap().Applied()
	if last := applied[len(applied)-1]; slices.Contains(last.Parts, fake.PartCache) || !slices.Contains(last.Remove, fake.PartCache) {
		t.Errorf("Apply() = parts %v, remove %v, want %s removed: the run named it", last.Parts, last.Remove, fake.PartCache)
	}
}

func TestAPartProjectsWantThatTheBootstrapLacksIsDescribedAsWantedByThem(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.PartCache, "shop")
	recordWant(t, vendor, fake.PartCache, "blog")

	cache := describedParts(t, client)[fake.PartCache]

	if cache.GetState() != contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_WANTED || !slices.Equal(cache.GetWantedBy(), []string{"blog", "shop"}) {
		t.Errorf("%s = %+v, want blog and shop to want it", fake.PartCache, cache)
	}
	if cache.GetSummary() == "" || cache.GetAddedAt() != 0 || len(cache.GetUsedBy()) != 0 {
		t.Errorf("%s = %+v, want its summary, and nobody to have added or to use it", fake.PartCache, cache)
	}
}

func TestAWantOfAPartTheBootstrapHoldsDescribesThePartAlone(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	bootstrapped(t, vendor, environment.TierProduction, fake.PartCache)
	recordWant(t, vendor, fake.PartCache, "shop")

	cache := describedParts(t, client)[fake.PartCache]

	if cache.GetState() != contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_READY || len(cache.GetWantedBy()) != 0 {
		t.Errorf("%s = %+v, want it ready and wanted by nobody: it is there", fake.PartCache, cache)
	}
}

func TestAWantOfAPartTheProviderNoLongerOffersIsLeftOut(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, "retired-part", "shop")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	applied := vendor.FakeBootstrap().Applied()
	if last := applied[len(applied)-1]; slices.Contains(last.Parts, "retired-part") {
		t.Errorf("Apply() parts = %v, want nothing this provider does not offer", last.Parts)
	}
}

func TestRemovingABootstrapForgetsWhatProjectsWantedOfIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.PartCache, "shop")

	stream, err := client.RemoveBootstrap(ctx, &contractv1.BootstrapScope{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil {
		t.Fatalf("RemoveBootstrap() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveBootstrap() = %q, %v", result.GetError(), err)
	}

	if wants := wantedParts(t, vendor, environment.TierProduction); len(wants) != 0 {
		t.Errorf("wants = %v, want none: the next bootstrap of this tier starts from nothing", wants)
	}
}

func TestADeployThroughAnEdgeIsRefusedByABootstrapLackingThePartsThatEdgeRequires(t *testing.T) {
	builtApps(t, "web")
	client, _ := contractServed(t, "1.0.0")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}})

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	result, _ := deploy(t, client, req)
	if result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want it refused for the parts its edge requires")
	}
	if want := "lacks a part this project wants: " + fake.PartImages; !strings.Contains(result.GetError(), want) {
		t.Errorf("Deploy() error = %q, want it to name %q", result.GetError(), want)
	}
}

func TestADeployTheProviderRefusesForAPartItWantsLeavesTheWantForTheBootstrapCommandToAdd(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	refusingForAWantedPart(vendor)

	if result, _ := deploy(t, client, deployRequest()); result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want it refused for the part the provider wants")
	}

	if got := wantedParts(t, vendor, environment.TierProduction); !slices.Equal(got[fake.PartCache], []string{"shop"}) {
		t.Errorf("wants = %v, want shop to want %s", got, fake.PartCache)
	}
}

func TestARefusedDeployLeavesTheWantForTheBootstrapCommandToAdd(t *testing.T) {
	builtApps(t, "web")
	client, vendor := contractServed(t, "1.0.0")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}})

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	if result, _ := deploy(t, client, req); result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want it refused for the parts its edge requires")
	}

	wants, err := stackrecords.ListBootstrapWants(context.Background(), vendor.KeyValues(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if got := wants[fake.PartImages]; !slices.Equal(got, []string{"shop"}) {
		t.Errorf("projects wanting %s = %v, want [shop]", fake.PartImages, got)
	}
}
