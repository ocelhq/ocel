package server

import (
	"context"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func wantNoProvisioningStage(t *testing.T, declared []recordedDeclare) {
	t.Helper()
	for _, batch := range declared {
		for _, stage := range batch.stages {
			if stage.Title == "Provisioning" || stage.Title == "Preparing" || stage.Title == "Uploading" || stage.Title == "Finalizing" {
				t.Errorf("teardown declared a deploy-shaped stage %q", stage.Title)
			}
		}
	}
}

func TestRunDestroyProjectDeclaresTheStagePlanBeforeAnyWork(t *testing.T) {
	t.Parallel()

	s := &Server{}
	tracer := &fakeTracer{}
	req := &contractv1.ProjectRequest{Slug: "shop"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.runDestroyProject(ctx, req, tracer, noopStageReport, noopLog)
	if err == nil {
		t.Fatal("runDestroyProject() error = nil, want the first reach for AWS to fail")
	}

	if len(tracer.declared) == 0 {
		t.Fatal("no stage plan was declared before runDestroyProject failed")
	}
	first := tracer.declared[0]
	if first.final {
		t.Error("first declared batch has Final = true, want false: the stacks to destroy are not known yet")
	}
	wantTitles := []string{
		"Planning the teardown",
		"Unbinding what routes to this project",
		"Destroying app stacks",
		"Destroying infra stacks",
		"Destroying the edge stack, its domain surfaces and the deployments ledger",
		"Purging stored variable values",
		"Purging project assets",
		"Forgetting the project",
	}
	if len(first.stages) != len(wantTitles) {
		t.Fatalf("got %d top-level stages, want %d", len(first.stages), len(wantTitles))
	}
	for i, want := range wantTitles {
		if got := first.stages[i].Title; got != want {
			t.Errorf("stage[%d].Title = %q, want %q", i, got, want)
		}
	}
	wantNoProvisioningStage(t, tracer.declared)

	if len(tracer.spans) != 0 {
		t.Fatalf("got %d spans, want 0: the failure happened before any declared stage started, and a stage that never ran must not get a fabricated span", len(tracer.spans))
	}

	last := tracer.declared[len(tracer.declared)-1]
	if !last.final || len(last.stages) != 0 {
		t.Errorf("final declaration = %+v, want an empty final batch closing the tree", last)
	}
}

func TestRunDestroyPreviewDeclaresTheStagePlanBeforeAnyWork(t *testing.T) {
	t.Parallel()

	t.Run("ephemeral preview has no infra stage", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		tracer := &fakeTracer{}
		req := &contractv1.RemoveEnvironmentRequest{
			Slug:        "shop",
			Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-1"},
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := s.runDestroyPreview(ctx, req, tracer, noopStageReport, noopLog); err == nil {
			t.Fatal("runDestroyPreview() error = nil, want the first reach for AWS to fail")
		}

		if len(tracer.declared) == 0 {
			t.Fatal("no stage plan was declared before runDestroyPreview failed")
		}
		first := tracer.declared[0]
		wantTitles := []string{"Removing the preview pointer", "Reclaiming deployments"}
		if len(first.stages) != len(wantTitles) {
			t.Fatalf("got %d root stages, want %d (no persistent infra stack)", len(first.stages), len(wantTitles))
		}
		wantNoProvisioningStage(t, tracer.declared)
	})

	t.Run("persistent preview also declares its infra stage", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		tracer := &fakeTracer{}
		req := &contractv1.RemoveEnvironmentRequest{
			Slug: "shop",
			Environment: &environmentv1.Environment{
				Tier:      environmentv1.Tier_TIER_PREVIEW,
				Identity:  "staging",
				Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if err := s.runDestroyPreview(ctx, req, tracer, noopStageReport, noopLog); err == nil {
			t.Fatal("runDestroyPreview() error = nil, want the first reach for AWS to fail")
		}

		first := tracer.declared[0]
		if len(first.stages) != 3 {
			t.Fatalf("got %d root stages, want 3 (pointer, reclaim, infra)", len(first.stages))
		}
	})
}

func TestRunPruneDeclaresTheStagePlanBeforeAnyWork(t *testing.T) {
	t.Parallel()

	s := &Server{}
	tracer := &fakeTracer{}
	req := &contractv1.RemoveStalePromotionsRequest{Slug: "shop"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.runPrune(ctx, req, tracer, noopStageReport, noopLog); err == nil {
		t.Fatal("runPrune() error = nil, want the first reach for AWS to fail")
	}

	if len(tracer.declared) == 0 {
		t.Fatal("no stage plan was declared before runPrune failed")
	}
	first := tracer.declared[0]
	wantTitles := []string{"Diffing deployments to reclaim", "Reclaiming deployments"}
	if len(first.stages) != len(wantTitles) {
		t.Fatalf("got %d root stages, want %d", len(first.stages), len(wantTitles))
	}
	for i, want := range wantTitles {
		if got := first.stages[i].Title; got != want {
			t.Errorf("stage[%d].Title = %q, want %q", i, got, want)
		}
	}
	wantNoProvisioningStage(t, tracer.declared)
}
