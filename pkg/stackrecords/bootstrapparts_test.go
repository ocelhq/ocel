package stackrecords_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var addedAt = time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)

func claimOf(addedBy string) stackrecords.BootstrapPart {
	return stackrecords.BootstrapPart{WrittenBy: "1.2.0", AddedBy: addedBy, AddedAt: addedAt}
}

func TestClaimingAMissingPartRecordsItAsCreatingByWhoeverAddedIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()

	got, claimed, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", claimOf("web"))
	if err != nil || !claimed {
		t.Fatalf("ClaimBootstrapPart() = %+v, %v, %v, want the part claimed", got, claimed, err)
	}

	read, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "containers")
	want := stackrecords.BootstrapPart{State: stackrecords.PartCreating, WrittenBy: "1.2.0", AddedBy: "web", AddedAt: addedAt}
	if err != nil || !present || !reflect.DeepEqual(read, want) {
		t.Errorf("ReadBootstrapPart() = %+v, %v, %v, want %+v", read, present, err, want)
	}
}

func TestASecondClaimOfAPartLosesAndReadsTheRecordTheFirstWrote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if _, _, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", claimOf("web")); err != nil {
		t.Fatal(err)
	}

	got, claimed, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", claimOf("api"))

	if err != nil || claimed || got.AddedBy != "web" || got.State != stackrecords.PartCreating {
		t.Errorf("ClaimBootstrapPart() = %+v, %v, %v, want the claim lost to web's", got, claimed, err)
	}
}

func TestAClaimThatRacesAnotherWriterLosesToIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	key := stackrecords.BootstrapPartKey(environment.TierProduction, "containers")
	store.BeforeNextWrite(key, func() {
		if _, _, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", claimOf("api")); err != nil {
			t.Error(err)
		}
	})

	got, claimed, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", claimOf("web"))

	if err != nil || claimed || got.AddedBy != "api" {
		t.Errorf("ClaimBootstrapPart() = %+v, %v, %v, want the claim lost to api's, written first", got, claimed, err)
	}
}

func TestMarkingAClaimedPartReadyKeepsWhoAddedItAndWhen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if _, _, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", claimOf("web")); err != nil {
		t.Fatal(err)
	}

	resources := []provider.PartResource{{Kind: "AWS::ECS::Cluster", ID: "ocel-containers-production"}}
	if err := stackrecords.MarkBootstrapPartReady(ctx, store, environment.TierProduction, "containers", stackrecords.BootstrapPart{
		WrittenBy: "1.2.0", Digest: "d1", AddedBy: "api", AddedAt: addedAt.Add(time.Hour), Resources: resources,
	}); err != nil {
		t.Fatal(err)
	}

	got, _, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "containers")
	want := stackrecords.BootstrapPart{State: stackrecords.PartReady, WrittenBy: "1.2.0", Digest: "d1", AddedBy: "web", AddedAt: addedAt, Resources: resources}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("ReadBootstrapPart() = %+v, %v, want %+v", got, err, want)
	}
}

func TestMarkingAnUnrecordedPartReadyRecordsItAsAddedByTheMarker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()

	if err := stackrecords.MarkBootstrapPartReady(ctx, store, environment.TierPreview, "base", claimOf("")); err != nil {
		t.Fatal(err)
	}

	got, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierPreview, "base")
	want := stackrecords.BootstrapPart{State: stackrecords.PartReady, WrittenBy: "1.2.0", AddedAt: addedAt}
	if err != nil || !present || !reflect.DeepEqual(got, want) {
		t.Errorf("ReadBootstrapPart() = %+v, %v, %v, want %+v", got, present, err, want)
	}
}

func TestAnOlderBuildNeverRewritesAPartANewerBuildWrote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	newer := stackrecords.BootstrapPart{WrittenBy: "1.3.0", Digest: "new", AddedAt: addedAt}
	if err := stackrecords.MarkBootstrapPartReady(ctx, store, environment.TierProduction, "base", newer); err != nil {
		t.Fatal(err)
	}

	if err := stackrecords.MarkBootstrapPartReady(ctx, store, environment.TierProduction, "base", stackrecords.BootstrapPart{WrittenBy: "1.2.0", Digest: "old", AddedAt: addedAt}); err != nil {
		t.Fatal(err)
	}

	got, _, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "base")
	if err != nil || got.WrittenBy != "1.3.0" || got.Digest != "new" {
		t.Errorf("ReadBootstrapPart() = %+v, %v, want what 1.3.0 wrote", got, err)
	}
}

func TestListingATiersPartsReadsEveryPartOfThatTierAndNoneOfTheOther(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	for tier, names := range map[environment.Tier][]string{
		environment.TierProduction: {"base", "containers"},
		environment.TierPreview:    {"base"},
	} {
		for _, name := range names {
			if err := stackrecords.MarkBootstrapPartReady(ctx, store, tier, name, claimOf("")); err != nil {
				t.Fatal(err)
			}
		}
	}

	got, err := stackrecords.ListBootstrapParts(ctx, store, environment.TierProduction)

	if err != nil || len(got) != 2 || got["base"].State != stackrecords.PartReady || got["containers"].State != stackrecords.PartReady {
		t.Errorf("ListBootstrapParts() = %+v, %v, want production's base and containers", got, err)
	}
}

func TestAForgottenPartIsMissingAndCanBeClaimedAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.MarkBootstrapPartReady(ctx, store, environment.TierProduction, "containers", claimOf("web")); err != nil {
		t.Fatal(err)
	}

	if err := stackrecords.ForgetBootstrapPart(ctx, store, environment.TierProduction, "containers"); err != nil {
		t.Fatal(err)
	}

	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "containers"); err != nil || present {
		t.Errorf("ReadBootstrapPart() after ForgetBootstrapPart() = %v, %v, want it missing", present, err)
	}
	if _, claimed, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", claimOf("api")); err != nil || !claimed {
		t.Errorf("ClaimBootstrapPart() after ForgetBootstrapPart() = %v, %v, want it claimed", claimed, err)
	}
}

func TestReleasingAClaimForgetsItButLeavesAPartAnotherRunMadeReady(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	claim, _, err := stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", claimOf("web"))
	if err != nil {
		t.Fatal(err)
	}

	if err := stackrecords.ReleaseBootstrapPart(ctx, store, environment.TierProduction, "containers", claim); err != nil {
		t.Fatalf("ReleaseBootstrapPart() error = %v", err)
	}
	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "containers"); err != nil || present {
		t.Errorf("ReadBootstrapPart() present = %v, %v after the claim was released, want it forgotten", present, err)
	}

	claim, _, err = stackrecords.ClaimBootstrapPart(ctx, store, environment.TierProduction, "containers", claimOf("web"))
	if err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.MarkBootstrapPartReady(ctx, store, environment.TierProduction, "containers", claimOf("api")); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.ReleaseBootstrapPart(ctx, store, environment.TierProduction, "containers", claim); err != nil {
		t.Fatalf("ReleaseBootstrapPart() error = %v", err)
	}
	if read, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "containers"); err != nil || !present || read.State != stackrecords.PartReady {
		t.Errorf("ReadBootstrapPart() = %+v, %v, %v, want the ready part kept", read, present, err)
	}
}
