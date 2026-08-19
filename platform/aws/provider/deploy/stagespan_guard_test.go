package deploy

import (
	"context"
	"errors"
	"testing"

	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func spanCounts(spans []spanCall) map[StageID]int {
	counts := make(map[StageID]int, len(spans))
	for _, s := range spans {
		counts[s.id]++
	}
	return counts
}

func requireExactlyOneSpanEach(t *testing.T, spans []spanCall, roots []Stage) {
	t.Helper()
	counts := spanCounts(spans)
	for _, root := range roots {
		if got := counts[root.ID]; got != 1 {
			t.Errorf("root stage %q got %d spans, want exactly 1", root.Title, got)
		}
	}
}

func newProjectTeardownStages() ProjectTeardownStages {
	return ProjectTeardownStages{
		Planning:    NewRootStage("Planning"),
		Unbind:      NewRootStage("Unbind"),
		Edge:        NewRootStage("Edge"),
		AppStacks:   NewRootStage("AppStacks"),
		InfraStacks: NewRootStage("InfraStacks"),
		Values:      NewRootStage("Values"),
		Assets:      NewRootStage("Assets"),
		Forget:      NewRootStage("Forget"),
	}
}

func TestDestroyProjectSpansEveryDeclaredRootStage(t *testing.T) {
	t.Parallel()

	t.Run("a clean run spans every root exactly once", func(t *testing.T) {
		t.Parallel()

		ft := &fakeTracer{}
		stages := newProjectTeardownStages()
		cfg := Config{Stacks: &fakeStackIndex{projects: []string{"shop"}}, Tracer: ft}

		if _, err := DestroyProject(context.Background(), nil, cfg, "shop", stages, nil); err != nil {
			t.Fatalf("DestroyProject: %v", err)
		}
		requireExactlyOneSpanEach(t, ft.spans, stages.Roots())
	})

	t.Run("a run with internal failures still spans every root exactly once", func(t *testing.T) {
		t.Parallel()

		ft := &fakeTracer{}
		stages := newProjectTeardownStages()
		cfg := Config{
			Stacks: &fakeStackIndex{projects: []string{"shop"}},
			Tracer: ft,
			Values: &valueRecorder{err: errors.New("table is on fire")},
		}

		if _, err := DestroyProject(context.Background(), nil, cfg, "shop", stages, nil); err == nil {
			t.Fatal("DestroyProject err = nil, want the failed value removal reported")
		}
		requireExactlyOneSpanEach(t, ft.spans, stages.Roots())
	})
}

func newPreviewRemovalStagesForTest(persistent bool) PreviewRemovalStages {
	stages := PreviewRemovalStages{
		Pointer: NewRootStage("Pointer"),
		Reclaim: NewRootStage("Reclaim"),
	}
	if persistent {
		stages.Infra = NewRootStage("Infra")
	}
	return stages
}

func TestRemovePreviewSpansEveryDeclaredRootStage(t *testing.T) {
	t.Parallel()

	t.Run("a clean run spans every root exactly once", func(t *testing.T) {
		t.Parallel()

		ft := &fakeTracer{}
		stages := newPreviewRemovalStagesForTest(false)
		fake := &recordingEdge{kind: cloudflare.Kind}
		ctx := context.Background()
		state := fake.reconciled(t, edge.StackSpec{Version: "v1", Slug: "shop"})

		if err := RemovePreview(ctx, state, Config{Tracer: ft}, "shop", "pr-1", false, stages, nil); err != nil {
			t.Fatalf("RemovePreview: %v", err)
		}
		requireExactlyOneSpanEach(t, ft.spans, stages.Roots())
	})

	t.Run("a failed pointer removal still spans every root exactly once", func(t *testing.T) {
		t.Parallel()

		ft := &fakeTracer{}
		stages := newPreviewRemovalStagesForTest(false)
		fake := &recordingEdge{kind: cloudflare.Kind}
		stale := edge.StackState{edge.StackKeySlug: "shop", edge.StackKeySecret: "stale"}

		if err := RemovePreview(context.Background(), fake.opened(t, stale), Config{Tracer: ft}, "shop", "pr-1", false, stages, nil); err == nil {
			t.Fatal("RemovePreview err = nil, want the refused pointer removal reported")
		}
		requireExactlyOneSpanEach(t, ft.spans, stages.Roots())
	})

	t.Run("a persistent preview also spans its infra root", func(t *testing.T) {
		t.Parallel()

		ft := &fakeTracer{}
		stages := newPreviewRemovalStagesForTest(true)
		fake := &recordingEdge{kind: cloudflare.Kind}
		ctx := context.Background()
		state := fake.reconciled(t, edge.StackSpec{Version: "v1", Slug: "shop"})

		if err := RemovePreview(ctx, state, Config{Tracer: ft}, "shop", "staging", true, stages, nil); err == nil {
			t.Fatal("RemovePreview err = nil, want the infra stack's stack-less Destroy to fail fast")
		}
		requireExactlyOneSpanEach(t, ft.spans, stages.Roots())
	})
}

func TestPruneSpansTheStagesItActuallyRuns(t *testing.T) {
	t.Parallel()

	t.Run("a clean run spans both Diff and Reclaim", func(t *testing.T) {
		t.Parallel()

		ft := &fakeTracer{}
		stages := PruneStages{Diff: NewRootStage("Diff"), Reclaim: NewRootStage("Reclaim")}
		fake := &recordingEdge{kind: cloudflare.Kind}
		ctx := context.Background()
		state := fake.reconciled(t, edge.StackSpec{Version: "v1", Slug: "shop"})

		if _, err := Prune(ctx, state, Config{Tracer: ft}, "shop", 3, "", stages, nil); err != nil {
			t.Fatalf("Prune: %v", err)
		}
		requireExactlyOneSpanEach(t, ft.spans, []Stage{stages.Diff, stages.Reclaim})
	})

	t.Run("a Diff failure spans only Diff — Reclaim never started", func(t *testing.T) {
		t.Parallel()

		ft := &fakeTracer{}
		stages := PruneStages{Diff: NewRootStage("Diff"), Reclaim: NewRootStage("Reclaim")}
		fake := &recordingEdge{kind: cloudflare.Kind}
		stale := edge.StackState{edge.StackKeySlug: "shop", edge.StackKeySecret: "stale"}

		if _, err := Prune(context.Background(), fake.opened(t, stale), Config{Tracer: ft}, "shop", 3, "", stages, nil); err == nil {
			t.Fatal("Prune err = nil, want the refused diff reported")
		}
		counts := spanCounts(ft.spans)
		if counts[stages.Diff.ID] != 1 {
			t.Errorf("Diff got %d spans, want exactly 1", counts[stages.Diff.ID])
		}
		if counts[stages.Reclaim.ID] != 0 {
			t.Errorf("Reclaim got %d spans, want 0: it never started once Diff failed", counts[stages.Reclaim.ID])
		}
	})
}

func TestChildStagesForPreservesDeclarationOrder(t *testing.T) {
	t.Parallel()

	parent := NewRootStage("AppStacks")
	stacks := appStacks("z", "a", "m", "b", "y")

	_, ordered := childStagesFor(parent, stacks)

	if len(ordered) != len(stacks) {
		t.Fatalf("got %d ordered stages, want %d", len(ordered), len(stacks))
	}
	for i, stack := range stacks {
		if want := stack.String(); ordered[i].Title != want {
			t.Errorf("ordered[%d].Title = %q, want %q (declaration order must match plan order, not map order)", i, ordered[i].Title, want)
		}
	}
}
