package site

import (
	"context"
	"testing"

	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/ocelhq/ocel/pkg/progress"
)

func createRequest(args assetSetArgs, planning bool) infer.CreateRequest[assetSetArgs] {
	return infer.CreateRequest[assetSetArgs]{Inputs: args, DryRun: planning}
}

func TestAStaticAssetSetIsPlannedOnceAndPushedOnce(t *testing.T) {
	t.Parallel()

	cfg := assetShippingConfig(t, "web")
	cfg.buckets.Objects = &fakeArtifactStore{exists: map[string]bool{}}
	coord := storageCoordinate("prod", "shop", "web", fixedRelease(t))

	first, err := staticAssetSet(cfg.buckets, cfg.root, "web", nextStatic, coord)
	if err != nil {
		t.Fatalf("staticAssetSet: %v", err)
	}
	second, err := staticAssetSet(cfg.buckets, cfg.root, "web", nextStatic, coord)
	if err != nil {
		t.Fatalf("staticAssetSet: %v", err)
	}
	if first.digest != second.digest {
		t.Errorf("the same build digests to %q then %q, want one digest so an unchanged set is not re-pushed", first.digest, second.digest)
	}
	if first.files == 0 {
		t.Error("the set counts no files, and the plan's row would say nothing about what it contains")
	}
}

func TestAnAssetSetTheRunNeverQueuedIsRefused(t *testing.T) {
	t.Parallel()

	pending := NewPendingSets()
	resource := &assetSetResource{pending: pending}
	if _, err := resource.Create(context.Background(), createRequest(assetSetArgs{
		Stack: "prod.web.rel-1", Set: StaticAssetSetName,
	}, false)); err == nil {
		t.Error("Create() of a set the run has nothing pending for = nil, want a refusal: the plan's row would go unwritten")
	}
}

func TestAPlannedAssetSetPushesNothing(t *testing.T) {
	t.Parallel()

	pending := NewPendingSets()
	pushed := 0
	pending.Add("prod.web.rel-1", []AssetSet{{
		name: StaticAssetSetName,
		push: func(context.Context, progress.Log) error { pushed++; return nil },
	}}, nil)

	resource := &assetSetResource{pending: pending}
	if _, err := resource.Create(context.Background(), createRequest(assetSetArgs{
		Stack: "prod.web.rel-1", Set: StaticAssetSetName,
	}, true)); err != nil {
		t.Fatalf("Create() during a plan = %v", err)
	}
	if pushed != 0 {
		t.Errorf("a planned set pushed %d times, want none", pushed)
	}

	if _, err := resource.Create(context.Background(), createRequest(assetSetArgs{
		Stack: "prod.web.rel-1", Set: StaticAssetSetName,
	}, false)); err != nil {
		t.Fatalf("Create() during an apply = %v", err)
	}
	if pushed != 1 {
		t.Errorf("an applied set pushed %d times, want the one the plan showed", pushed)
	}
}
