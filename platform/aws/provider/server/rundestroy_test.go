package server

import (
	"context"
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func noopStageReport(deploy.StageID) func(string) { return noopLog }

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
	req := &deploymentsv1.DestroyProjectRequest{Slug: "shop", Options: []byte("not json")}

	err := s.runDestroyProject(context.Background(), req, tracer, noopStageReport, noopLog)
	if err == nil {
		t.Fatal("runDestroyProject() error = nil, want the parseOptions failure")
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
		"Destroying edge workers and the deployments store",
		"Destroying app stacks",
		"Destroying infra stacks",
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

	if len(tracer.spans) != len(wantTitles) {
		t.Fatalf("got %d spans, want one per declared root stage since the failure happened before any of them ran", len(tracer.spans))
	}
	for _, span := range tracer.spans {
		if !span.failed {
			t.Errorf("span %v was not recorded as failed", span.id)
		}
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
		req := &deploymentsv1.DestroyPreviewRequest{
			Slug:        "shop",
			Options:     []byte("not json"),
			Environment: &deploymentsv1.Environment{Class: deploymentsv1.Environment_CLASS_PREVIEW, Identity: "pr-1"},
		}

		if err := s.runDestroyPreview(context.Background(), req, tracer, noopStageReport, noopLog); err == nil {
			t.Fatal("runDestroyPreview() error = nil, want the parseOptions failure")
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
		req := &deploymentsv1.DestroyPreviewRequest{
			Slug:    "shop",
			Options: []byte("not json"),
			Environment: &deploymentsv1.Environment{
				Class:     deploymentsv1.Environment_CLASS_PREVIEW,
				Identity:  "staging",
				Lifecycle: deploymentsv1.Environment_LIFECYCLE_PERSISTENT,
			},
		}

		if err := s.runDestroyPreview(context.Background(), req, tracer, noopStageReport, noopLog); err == nil {
			t.Fatal("runDestroyPreview() error = nil, want the parseOptions failure")
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
	req := &deploymentsv1.PruneRequest{Slug: "shop", Options: []byte("not json")}

	if _, err := s.runPrune(context.Background(), req, tracer, noopStageReport, noopLog); err == nil {
		t.Fatal("runPrune() error = nil, want the parseOptions failure")
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
