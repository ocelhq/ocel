package providerserver_test

import (
	"context"
	"slices"
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
	recordWant(t, vendor, fake.FeatureCache, "shop")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	applied := vendor.FakeBootstrap().Applied()
	if last := applied[len(applied)-1]; !slices.Contains(last.Features, fake.FeatureCache) {
		t.Errorf("Apply() features = %v, want %s, which shop wants", last.Features, fake.FeatureCache)
	}
	recorded, present, err := stackrecords.ReadBootstrapPart(context.Background(), vendor.KeyValues(), environment.TierProduction, fake.FeatureCache)
	if err != nil || !present || recorded.State != stackrecords.PartReady || recorded.AddedBy != "shop" {
		t.Errorf("ReadBootstrapPart(%s) = %+v, %v, %v, want it ready and added for shop", fake.FeatureCache, recorded, present, err)
	}
}

func TestAPartABootstrapAddsIsAddedByTheFirstProjectThatWantedIt(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.FeatureCache, "shop")
	time.Sleep(time.Millisecond)
	recordWant(t, vendor, fake.FeatureCache, "blog")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	recorded, _, err := stackrecords.ReadBootstrapPart(context.Background(), vendor.KeyValues(), environment.TierProduction, fake.FeatureCache)
	if err != nil || recorded.AddedBy != "shop" {
		t.Errorf("ReadBootstrapPart(%s) = %+v, %v, want it added by shop, which wanted it before blog", fake.FeatureCache, recorded, err)
	}
}

func TestAPartABootstrapAddedIsNoLongerWanted(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.FeatureCache, "shop")
	recordWant(t, vendor, fake.FeatureCache, "blog")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	if wants := wantedParts(t, vendor, environment.TierProduction); len(wants) != 0 {
		t.Errorf("wants = %v, want none: the bootstrap added every part they named", wants)
	}
}

func TestADryBootstrapPlansTheWantedPartsAndRecordsNothing(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.FeatureCache, "shop")

	plan := streamedPlan(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct, Dry: true})

	if !slices.ContainsFunc(plan.GetGroups(), func(group *planv1.ChangeGroup) bool { return group.GetFeature() == fake.FeatureCache }) {
		t.Errorf("plan groups = %v, want one adding %s", plan.GetGroups(), fake.FeatureCache)
	}
	if wants := wantedParts(t, vendor, environment.TierProduction); !slices.Equal(wants[fake.FeatureCache], []string{"shop"}) {
		t.Errorf("wants = %v, want shop's want of %s kept by a dry run", wants, fake.FeatureCache)
	}
}

func TestABootstrapRemovingAPartAProjectWantsRemovesItAndAddsNothing(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct, Features: []string{fake.FeatureCache}})
	recordWant(t, vendor, fake.FeatureCache, "shop")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct, Remove: []string{fake.FeatureCache}, Force: true})

	applied := vendor.FakeBootstrap().Applied()
	if last := applied[len(applied)-1]; slices.Contains(last.Features, fake.FeatureCache) || !slices.Contains(last.Remove, fake.FeatureCache) {
		t.Errorf("Apply() = features %v, remove %v, want %s removed: the run named it", last.Features, last.Remove, fake.FeatureCache)
	}
}

func TestAWantOfAPartTheProviderNoLongerOffersIsLeftOut(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, "retired-part", "shop")

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	applied := vendor.FakeBootstrap().Applied()
	if last := applied[len(applied)-1]; slices.Contains(last.Features, "retired-part") {
		t.Errorf("Apply() features = %v, want nothing this provider does not offer", last.Features)
	}
}
