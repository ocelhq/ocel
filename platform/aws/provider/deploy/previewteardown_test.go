package deploy

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
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
		targets, err := ReclaimTargets("shop", "pr-1", []string{"record:web/b1", "record:api/b2"}, nil, nil)
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
		if got, want := byApp["web"].Stack, appStack(t, "pr-1", "web", buildOnly("b1")); got != want {
			t.Errorf("web stack = %q, want the pointer-scoped preview stack %q", got, want)
		}
		prod, err := ReclaimTargets("shop", ProductionEnv, []string{"record:web/b1"}, nil, nil)
		if err != nil {
			t.Fatalf("ReclaimTargets(production): %v", err)
		}
		if prod[0].Stack == byApp["web"].Stack {
			t.Error("preview and production reclaim resolved the same stack name")
		}
		if prod[0].Stack != appStack(t, ProductionEnv, "web", buildOnly("b1")) {
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

func TestPreviewProjectWorkers(t *testing.T) {
	t.Parallel()

	t.Run("reclaims the whole worker family", func(t *testing.T) {
		t.Parallel()
		got := previewProjectWorkers("shop", []string{
			previewWorkerStem("shop") + "--web",
			previewWorkerName("shop"),
			previewWorkerStem("shop") + "--api",
		})
		want := []string{
			"ocel--shop--preview--api",
			"ocel--shop--preview--root",
			"ocel--shop--preview--web",
			"ocel-shop--preview",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("workers = %v, want %v", got, want)
		}
	})

	t.Run("an empty list still reclaims both entrypoints", func(t *testing.T) {
		t.Parallel()
		got := previewProjectWorkers("shop", nil)
		want := []string{"ocel--shop--preview--root", "ocel-shop--preview"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("workers = %v, want the current and retired entrypoints", got)
		}
	})

	t.Run("reclaims the retired family so the cutover strands nothing", func(t *testing.T) {
		t.Parallel()
		got := previewProjectWorkers("shop", []string{"ocel-shop--preview-web"})
		want := []string{"ocel--shop--preview--root", "ocel-shop--preview", "ocel-shop--preview-web"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("workers = %v, want %v", got, want)
		}
	})

	t.Run("never adopts a name outside the family", func(t *testing.T) {
		t.Parallel()
		got := previewProjectWorkers("shop", []string{
			previewWorkerName("shopfoo"),
			previewWorkerStem("other") + "--web",
			"ocel--shop--previewer",
			"ocel--shop-preview--prod--web",
			workerScriptName("shop", "prod", "web"),
			"my-worker",
		})
		want := []string{"ocel--shop--preview--root", "ocel-shop--preview"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("workers = %v, want %v", got, want)
		}
	})

	t.Run("no slug names nothing", func(t *testing.T) {
		t.Parallel()
		if got := previewProjectWorkers("", []string{"ocel--shop--preview--root"}); got != nil {
			t.Errorf("workers = %v, want none for a project with no slug", got)
		}
	})
}

func TestRemovePreview(t *testing.T) {
	t.Run("touches no project-level edge state", func(t *testing.T) {
		fake := &recordingRootStack{}
		ctx := context.Background()
		state, err := fake.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: "shop"}, nil)
		if err != nil {
			t.Fatalf("ReconcileRootStack: %v", err)
		}

		if err := RemovePreview(ctx, fake, state, Config{}, "shop", "pr-1", false, nil, nil); err != nil {
			t.Fatalf("RemovePreview: %v", err)
		}

		if !reflect.DeepEqual(fake.removedPointers, []string{"pr-1"}) {
			t.Errorf("removed pointers = %v, want [pr-1]", fake.removedPointers)
		}
		if fake.destroyed != 0 || len(fake.destroyedWorkers) != 0 {
			t.Errorf("destroyed workers %v: the entrypoint worker outlives every pointer", fake.destroyedWorkers)
		}
		if fake.destroyedInstance != 0 {
			t.Error("wiped the store instance: it outlives every pointer and only `ocel destroy --preview` retires it")
		}
	})

	t.Run("no root-stack state touches nothing", func(t *testing.T) {
		fake := &recordingRootStack{}

		if err := RemovePreview(context.Background(), fake, nil, Config{}, "shop", "pr-1", false, nil, nil); err != nil {
			t.Fatalf("RemovePreview: %v", err)
		}
		if fake.destroyed != 0 || len(fake.removedPointers) != 0 || fake.destroyedInstance != 0 {
			t.Errorf("RemovePreview touched the edge with no root-stack state: %+v", fake)
		}
	})

	t.Run("reports a failed pointer removal", func(t *testing.T) {
		fake := &recordingRootStack{}
		stale := edge.RootStackState{edge.RootStackKeySlug: "shop", edge.RootStackKeySecret: "stale"}

		err := RemovePreview(context.Background(), fake, stale, Config{}, "shop", "pr-1", false, nil, nil)
		if err == nil {
			t.Fatal("RemovePreview err = nil, want the refused removal reported")
		}
		if !strings.Contains(err.Error(), "pr-1") {
			t.Errorf("error %q does not name the pointer it failed to remove", err)
		}
		if fake.destroyed != 0 || fake.destroyedInstance != 0 {
			t.Errorf("touched the project's edge state: %+v", fake)
		}
	})
}
