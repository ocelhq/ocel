package surface_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

func TestARecordKeepsWhatAPatchSetUntilNothingIsLeftToReset(t *testing.T) {
	ctx := context.Background()
	store := fake.NewKeyValues()
	ref := provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: naming.InfraStack("prod")}
	record := surface.Record{Store: store, Key: surface.KeyFor(ref, "bucket", "uploads")}

	applied := surface.Recorded{Paths: []string{"storageClass"}, Labels: []string{"team"}}
	if err := record.Write(ctx, applied); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	read, err := record.Read(ctx)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if !slices.Equal(read.Paths, applied.Paths) || !slices.Equal(read.Labels, applied.Labels) {
		t.Errorf("Read() = %+v, want %+v back", read, applied)
	}

	if err := record.Write(ctx, surface.Recorded{}); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if len(store.Snapshot()) != 0 {
		t.Errorf("%d entries left once nothing is patched, want the record gone: a teardown leaves no bytes behind", len(store.Snapshot()))
	}
}
