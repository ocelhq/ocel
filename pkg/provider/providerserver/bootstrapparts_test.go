package providerserver_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var direct = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}

func describedParts(t *testing.T, client contractv1connect.ProviderServiceClient) map[string]*contractv1.BootstrapPart {
	t.Helper()
	described, err := client.DescribeBootstrap(context.Background(), &contractv1.DescribeBootstrapRequest{
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		WithParts: true,
	})
	if err != nil {
		t.Fatalf("DescribeBootstrap() error = %v", err)
	}
	parts := map[string]*contractv1.BootstrapPart{}
	for i, part := range described.GetParts() {
		if i == 0 && part.GetName() != provider.BasePart {
			t.Errorf("DescribeBootstrap() parts start with %q, want the base first", part.GetName())
		}
		parts[part.GetName()] = part
	}
	return parts
}

func TestABootstrapsPartsNameWhoAddedThemAndWhoUsesThem(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	recordProject(t, vendor, "shop", fake.PartImages)
	recordProject(t, vendor, "blog")

	parts := describedParts(t, client)

	base := parts[provider.BasePart]
	if base.GetState() != contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_READY || base.GetAddedBy() != "" || base.GetAddedAt() == 0 {
		t.Errorf("base = %+v, want it ready and added by `ocel bootstrap`", base)
	}
	if !slices.Equal(base.GetUsedBy(), []string{"blog", "shop"}) {
		t.Errorf("base used by %v, want every project recorded here", base.GetUsedBy())
	}
	if len(base.GetResources()) == 0 {
		t.Errorf("base = %+v, want the resources it holds", base)
	}
	images := parts[fake.PartImages]
	if !slices.Equal(images.GetUsedBy(), []string{"shop"}) {
		t.Errorf("%s used by %v, want the project that recorded it", fake.PartImages, images.GetUsedBy())
	}
}

func TestABootstrapRecordsWhatEachPartItCreatedHoldsAndTheDigestOfItsContent(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	for _, name := range []string{provider.BasePart, fake.PartImages} {
		recorded, present, err := stackrecords.ReadBootstrapPart(context.Background(), vendor.KeyValues(), environment.TierProduction, name)
		if err != nil || !present {
			t.Fatalf("ReadBootstrapPart(%s) = %v, %v, want the part recorded", name, present, err)
		}
		if len(recorded.Resources) == 0 || recorded.Resources[0].Kind == "" || recorded.Resources[0].ID == "" {
			t.Errorf("%s record holds resources %+v, want what the part holds", name, recorded.Resources)
		}
		if recorded.Digest == "" {
			t.Errorf("%s record holds no digest, want the digest of what the bootstrap wrote", name)
		}
	}
}

func TestAPartsIdleCostIsTheFixedMonthlyCostOfWhatItHoldsAndUnknownWithoutAShape(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	vendor.FakeBootstrap().AddPart(environment.TierProduction, provider.BootstrapPart{
		Name:   "containers",
		UsedBy: []string{"api"},
		Shape: &provider.PartShape{Vendor: string(fake.Vendor), Resources: []pricing.Shaped{
			{Name: "containers", Type: fake.TypeContainer, Properties: map[string]any{}},
		}},
	})
	vendor.FakeBootstrap().AddPart(environment.TierProduction, provider.BootstrapPart{Name: "bastion"})

	parts := describedParts(t, client)
	for _, name := range []string{provider.BasePart, "containers", "bastion"} {
		if parts[name] == nil {
			t.Fatalf("DescribeBootstrap() parts = %v, want %s among them", parts, name)
		}
	}

	if got := parts[provider.BasePart]; got.IdleMonthlyFixed == nil || got.GetIdleMonthlyFixed() != "0.00" {
		t.Errorf("base idle = %v, want 0.00: storage is billed by use", got.IdleMonthlyFixed)
	}
	containers := parts["containers"]
	if containers.GetIdleMonthlyFixed() != "7.30" || containers.GetCurrency() != "USD" {
		t.Errorf("containers idle = %s %s, want 7.30 USD for a container running every hour", containers.GetIdleMonthlyFixed(), containers.GetCurrency())
	}
	if !slices.Equal(containers.GetUsedBy(), []string{"api"}) {
		t.Errorf("containers used by %v, want the users its vendor reported", containers.GetUsedBy())
	}
	if parts["bastion"].IdleMonthlyFixed != nil {
		t.Errorf("bastion idle = %s, want it unknown: nothing shapes it", parts["bastion"].GetIdleMonthlyFixed())
	}
}

func TestAPartEveryProjectUsesIsUsedByEveryProjectRecordedInTheTier(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	vendor.FakeBootstrap().AddPart(environment.TierProduction, provider.BootstrapPart{Name: "runtime-layers", UsedByEveryProject: true})
	recordProject(t, vendor, "shop")
	recordProject(t, vendor, "blog")

	if got := describedParts(t, client)["runtime-layers"].GetUsedBy(); !slices.Equal(got, []string{"blog", "shop"}) {
		t.Errorf("runtime-layers used by %v, want every project recorded here", got)
	}
}

func TestAPartClaimedButNotYetCreatedReadsAsCreatingByWhoeverClaimedIt(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	if _, _, err := stackrecords.ClaimBootstrapPart(context.Background(), vendor.KeyValues(), environment.TierProduction, "containers",
		stackrecords.BootstrapPart{AddedBy: "web", AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	containers := describedParts(t, client)["containers"]

	if containers.GetState() != contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_CREATING || containers.GetAddedBy() != "web" {
		t.Errorf("containers = %+v, want it creating, claimed by web", containers)
	}
}

func TestABootstrapThatFailsLeavesNoPartItClaimedCreating(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	recordWant(t, vendor, fake.PartCache, "shop")
	vendor.FakeBootstrap().RefuseApply(errors.New("the stack update timed out"))

	stream, err := client.Bootstrap(context.Background(), &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	if result, _ := drain(stream); result.GetSuccess() {
		t.Fatal("Bootstrap() succeeded, want the refused apply to fail it")
	}

	if part := describedParts(t, client)[fake.PartCache]; part.GetState() == contractv1.BootstrapPartState_BOOTSTRAP_PART_STATE_CREATING {
		t.Errorf("parts hold %+v after the bootstrap adding it failed, want no claim left creating", part)
	}
}

func TestARemovedPartLeavesNoPartBehind(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})

	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Remove: []string{fake.PartCache}, Force: true, Edge: direct})

	if part, kept := describedParts(t, client)[fake.PartCache]; kept {
		t.Errorf("parts hold %+v after its part was removed", part)
	}
}

func TestADescribeThatDoesNotAskForPartsReadsNone(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	described, err := client.DescribeBootstrap(context.Background(), &contractv1.DescribeBootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	if err != nil || len(described.GetParts()) != 0 {
		t.Errorf("DescribeBootstrap() = %v parts, %v, want none read", len(described.GetParts()), err)
	}
}
