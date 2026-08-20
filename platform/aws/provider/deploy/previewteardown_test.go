package deploy

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPreviewInfraStackFor(t *testing.T) {
	t.Parallel()

	t.Run("persistent gets a stack", func(t *testing.T) {
		t.Parallel()
		persistent := PreviewInfraStackFor("staging", true)
		if want := naming.InfraStack("staging"); persistent != want {
			t.Errorf("persistent infra stack = %q, want %q", persistent, want)
		}
	})

	t.Run("ephemeral gets none", func(t *testing.T) {
		t.Parallel()
		if got := PreviewInfraStackFor("pr-1", false); !got.IsZero() {
			t.Errorf("ephemeral infra stack = %q, want none (ephemeral previews own no infra stack)", got)
		}
	})
}

func TestReclaimTargetsScopeByEnv(t *testing.T) {
	t.Parallel()

	t.Run("a preview env names pointer-scoped stacks", func(t *testing.T) {
		t.Parallel()
		targets, err := ReclaimTargets("shop", "pr-1", []string{recordKeyFor("web", deployedInto("pr-1", "b1", "")), recordKeyFor("api", deployedInto("pr-1", "b2", ""))}, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets: %v", err)
		}
		if len(targets) != 2 {
			t.Fatalf("targets = %d, want 2", len(targets))
		}
		byApp := map[string]PruneTarget{}
		for _, tg := range targets {
			byApp[tg.App] = tg
		}
		if got, want := byApp["web"].Stack, appStack(t, "pr-1", "web", deployedInto("pr-1", "b1", "")); got != want {
			t.Errorf("web stack = %q, want the pointer-scoped preview stack %q", got, want)
		}
		prod, err := ReclaimTargets("shop", ProductionEnv, []string{recordKeyFor("web", deployedAs("b1"))}, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets(production): %v", err)
		}
		if prod[0].Stack == byApp["web"].Stack {
			t.Error("preview and production reclaim resolved the same stack name")
		}
		if prod[0].Stack != appStack(t, ProductionEnv, "web", deployedAs("b1")) {
			t.Errorf("production stack = %q, want the production env's", prod[0].Stack)
		}
	})
}

func TestClassifyPreviewStacks(t *testing.T) {
	t.Parallel()

	t.Run("splits infra app and pointers", func(t *testing.T) {
		t.Parallel()
		got := classifyPreviewStacks([]naming.StackName{
			naming.InfraStack("staging"),
			naming.AppStack("staging", "web", testRelease(t, "b1")),
			naming.AppStack("pr-1", "web", testRelease(t, "b2")),
			naming.AppStack("pr-1", "api", testRelease(t, "b3")),
			naming.InfraStack(ProductionEnv),
			naming.AppStack(ProductionEnv, "web", testRelease(t, "b9")),
		})

		wantInfra := []naming.StackName{naming.InfraStack("staging")}
		wantApps := []naming.StackName{
			naming.AppStack("staging", "web", testRelease(t, "b1")),
			naming.AppStack("pr-1", "web", testRelease(t, "b2")),
			naming.AppStack("pr-1", "api", testRelease(t, "b3")),
		}
		if !reflect.DeepEqual(got.InfraStacks, wantInfra) {
			t.Errorf("InfraStacks = %v, want %v", got.InfraStacks, wantInfra)
		}
		if !reflect.DeepEqual(got.AppStacks, wantApps) {
			t.Errorf("AppStacks = %v, want %v", got.AppStacks, wantApps)
		}
		if !reflect.DeepEqual(got.Pointers, []string{"pr-1", "staging"}) {
			t.Errorf("Pointers = %v, want [pr-1 staging] (distinct, sorted)", got.Pointers)
		}
	})

	t.Run("excludes production", func(t *testing.T) {
		t.Parallel()
		got := classifyPreviewStacks([]naming.StackName{
			naming.InfraStack(ProductionEnv),
			naming.AppStack(ProductionEnv, "web", testRelease(t, "b1")),
		})
		if len(got.InfraStacks) != 0 || len(got.AppStacks) != 0 || len(got.Pointers) != 0 {
			t.Errorf("classifyPreviewStacks matched production stacks: %+v", got)
		}
	})
}

func TestPreviewPurgeEnvs(t *testing.T) {
	t.Parallel()

	plan := classifyPreviewStacks([]naming.StackName{
		naming.InfraStack("staging"),
		naming.AppStack("pr-1", "web", testRelease(t, "b1")),
	})

	t.Run("the named preview is purged even when the index is empty", func(t *testing.T) {
		t.Parallel()
		if got := previewPurgeEnvs(PreviewProjectTeardownPlan{}, "pr-7"); !reflect.DeepEqual(got, []string{"pr-7"}) {
			t.Errorf("envs = %v, want [pr-7]", got)
		}
	})

	t.Run("the named preview joins the ones the index knows", func(t *testing.T) {
		t.Parallel()
		if got := previewPurgeEnvs(plan, "pr-7"); !reflect.DeepEqual(got, []string{"pr-1", "pr-7", "staging"}) {
			t.Errorf("envs = %v, want [pr-1 pr-7 staging]", got)
		}
	})

	t.Run("a project-wide scope names no preview of its own", func(t *testing.T) {
		t.Parallel()
		if got := previewPurgeEnvs(plan, EveryPreview); !reflect.DeepEqual(got, []string{"pr-1", "staging"}) {
			t.Errorf("envs = %v, want the index's pointers only", got)
		}
		if got := previewPurgeEnvs(PreviewProjectTeardownPlan{}, EveryPreview); len(got) != 0 {
			t.Errorf("envs = %v, want none: a project-wide scope names no environment to fall back on", got)
		}
	})

	t.Run("production is never swept by a preview teardown", func(t *testing.T) {
		t.Parallel()
		if got := previewPurgeEnvs(plan, ProductionEnv); !reflect.DeepEqual(got, []string{"pr-1", "staging"}) {
			t.Errorf("envs = %v, want production left out", got)
		}
	})
}

func TestRemovePreview(t *testing.T) {
	t.Run("touches no project-level edge state", func(t *testing.T) {
		fake := &recordingEdge{kind: cloudflare.Kind}
		ctx := context.Background()
		state := fake.reconciled(t, edge.StackSpec{Version: "v1", Slug: "shop"})

		if err := RemovePreview(ctx, state, Reclamation{Teardown: Teardown{Slug: "shop"}}, "pr-1", false, PreviewRemovalStages{}, nil); err != nil {
			t.Fatalf("RemovePreview: %v", err)
		}

		if !reflect.DeepEqual(fake.removedPointers, []string{"pr-1"}) {
			t.Errorf("removed pointers = %v, want [pr-1]", fake.removedPointers)
		}
		if fake.destroyed != 0 {
			t.Error("destroyed the stack: the entrypoint worker and the store instance outlive every pointer, and only `ocel destroy --preview` retires them")
		}
	})

	t.Run("no root-stack state touches nothing", func(t *testing.T) {
		fake := &recordingEdge{kind: cloudflare.Kind}

		if err := RemovePreview(context.Background(), fake.opened(t, edge.StackState{}), Reclamation{Teardown: Teardown{Slug: "shop"}}, "pr-1", false, PreviewRemovalStages{}, nil); err != nil {
			t.Fatalf("RemovePreview: %v", err)
		}
		if fake.destroyed != 0 || len(fake.removedPointers) != 0 {
			t.Errorf("RemovePreview touched the edge with no root-stack state: %+v", fake)
		}
	})

	t.Run("reports a failed pointer removal", func(t *testing.T) {
		fake := &recordingEdge{kind: cloudflare.Kind}
		stale := edge.StackState{Slug: "shop", Secret: "stale"}

		err := RemovePreview(context.Background(), fake.opened(t, stale), Reclamation{Teardown: Teardown{Slug: "shop"}}, "pr-1", false, PreviewRemovalStages{}, nil)
		if err == nil {
			t.Fatal("RemovePreview err = nil, want the refused removal reported")
		}
		if !strings.Contains(err.Error(), "pr-1") {
			t.Errorf("error %q does not name the pointer it failed to remove", err)
		}
		if fake.destroyed != 0 {
			t.Errorf("touched the project's edge state: %+v", fake)
		}
	})
}
