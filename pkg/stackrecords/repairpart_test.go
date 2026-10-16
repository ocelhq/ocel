package stackrecords_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestRepairingRemakesACurrentRecordedPartAndKeepsWhoAddedIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, (&making{resources: syncResources}).work("web", "1.2.0", "d1")); err != nil {
		t.Fatal(err)
	}

	repair := &making{resources: syncResources}
	repaired, err := stackrecords.RepairBootstrapParts(ctx, store, environment.TierProduction, []stackrecords.PartWork{repair.work("ocel bootstrap", "1.2.0", "d1")})
	if err != nil {
		t.Fatalf("RepairBootstrapParts() = %v", err)
	}

	got, _, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync")
	if repair.calls.Load() != 1 || !slices.Equal(repaired, []string{"env-source-sync"}) || err != nil || got.State != stackrecords.PartReady || got.AddedBy != "web" {
		t.Errorf("made %d times, repaired %v, record %+v (%v); want a part current at its digest made again, still added by web", repair.calls.Load(), repaired, got, err)
	}
}

func TestRepairingMakesNoPartNothingRecorded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()

	repair := &making{resources: syncResources}
	repaired, err := stackrecords.RepairBootstrapParts(ctx, store, environment.TierProduction, []stackrecords.PartWork{repair.work("ocel bootstrap", "1.2.0", "d1")})
	if err != nil {
		t.Fatalf("RepairBootstrapParts() = %v", err)
	}

	if _, present, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync"); repair.calls.Load() != 0 || len(repaired) != 0 || err != nil || present {
		t.Errorf("made %d times, repaired %v, record present %v (%v); want nothing added: a part is added by its first use", repair.calls.Load(), repaired, present, err)
	}
}

func TestRepairingLeavesAPartANewerBuildWrote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := fake.NewKeyValues()
	if err := stackrecords.EnsureBootstrapPart(ctx, store, environment.TierProduction, (&making{}).work("web", "1.3.0", "new")); err != nil {
		t.Fatal(err)
	}

	older := &making{}
	repaired, err := stackrecords.RepairBootstrapParts(ctx, store, environment.TierProduction, []stackrecords.PartWork{older.work("ocel bootstrap", "1.2.0", "old")})
	if err != nil {
		t.Fatalf("RepairBootstrapParts() = %v", err)
	}

	got, _, err := stackrecords.ReadBootstrapPart(ctx, store, environment.TierProduction, "env-source-sync")
	if older.calls.Load() != 0 || len(repaired) != 0 || err != nil || got.Digest != "new" {
		t.Errorf("an older build made it %d times (repaired %v) and left %+v (%v), want what 1.3.0 wrote untouched", older.calls.Load(), repaired, got, err)
	}
}
