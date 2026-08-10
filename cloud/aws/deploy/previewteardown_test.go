package deploy

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

func TestPreviewInfraStackFor_PersistentGetsAStackEphemeralGetsNone(t *testing.T) {
	persistent := PreviewInfraStackFor("shop", "staging", true)
	if want := PreviewInfraStackName("shop", "staging"); persistent != want {
		t.Errorf("persistent infra stack = %q, want %q", persistent, want)
	}
	if got := PreviewInfraStackFor("shop", "pr-1", false); got != "" {
		t.Errorf("ephemeral infra stack = %q, want empty (ephemeral previews own no infra stack)", got)
	}
}

func TestPreviewReclaimTargets_UsePointerScopedStackNames(t *testing.T) {
	targets, err := PreviewReclaimTargets("shop", "pr-1", "preview-pr-1", []string{"record:web/b1", "record:api/b2"}, nil, nil)
	if err != nil {
		t.Fatalf("PreviewReclaimTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(targets))
	}
	byApp := map[string]PruneTarget{}
	for _, tg := range targets {
		byApp[tg.App] = tg
	}
	if got, want := byApp["web"].Stack, PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b1")); got != want {
		t.Errorf("web stack = %q, want the pointer-scoped preview stack %q", got, want)
	}
	// The production reclaim of the same record must resolve a different stack —
	// proving preview and production never collide on stack names.
	prod, _ := ReclaimTargets("shop", "prod", []string{"record:web/b1"}, nil, nil)
	if prod[0].Stack == byApp["web"].Stack {
		t.Error("preview and production reclaim resolved the same stack name")
	}
}

func TestReclaimTargetsFor_BranchesOnPointer(t *testing.T) {
	prod, err := reclaimTargetsFor("shop", "", "prod", []string{"record:web/b1"}, nil, nil)
	if err != nil {
		t.Fatalf("reclaimTargetsFor(production): %v", err)
	}
	if prod[0].Stack != AppDeployStackName("shop", "web", buildOnly("b1")) {
		t.Errorf("empty pointer must resolve production stacks, got %q", prod[0].Stack)
	}
	preview, err := reclaimTargetsFor("shop", "pr-1", "preview-pr-1", []string{"record:web/b1"}, nil, nil)
	if err != nil {
		t.Fatalf("reclaimTargetsFor(preview): %v", err)
	}
	if preview[0].Stack != PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b1")) {
		t.Errorf("named pointer must resolve preview stacks, got %q", preview[0].Stack)
	}
}

func TestClassifyPreviewStacks_SplitsInfraAppAndPointers(t *testing.T) {
	got := classifyPreviewStacks("shop", []string{
		PreviewInfraStackName("shop", "staging"),
		PreviewAppDeployStackName("shop", "staging", "web", buildOnly("b1")),
		PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b2")),
		PreviewAppDeployStackName("shop", "pr-1", "api", buildOnly("b3")),
		InfraStackName("shop"),                             // production infra — not a preview stack
		AppDeployStackName("shop", "web", buildOnly("b9")), // production app — not a preview stack
		"other--preview-x--web--b1",                        // another project's preview
	})

	sort.Strings(got.InfraStacks)
	sort.Strings(got.AppStacks)
	wantInfra := []string{PreviewInfraStackName("shop", "staging")}
	wantApps := []string{
		PreviewAppDeployStackName("shop", "pr-1", "api", buildOnly("b3")),
		PreviewAppDeployStackName("shop", "pr-1", "web", buildOnly("b2")),
		PreviewAppDeployStackName("shop", "staging", "web", buildOnly("b1")),
	}
	sort.Strings(wantApps)
	if !reflect.DeepEqual(got.InfraStacks, wantInfra) {
		t.Errorf("InfraStacks = %v, want %v", got.InfraStacks, wantInfra)
	}
	if !reflect.DeepEqual(got.AppStacks, wantApps) {
		t.Errorf("AppStacks = %v, want %v", got.AppStacks, wantApps)
	}
	if !reflect.DeepEqual(got.Pointers, []string{"pr-1", "staging"}) {
		t.Errorf("Pointers = %v, want [pr-1 staging] (distinct, sorted)", got.Pointers)
	}
}

// The entrypoint worker is the only preview worker a deploy names today, but an
// earlier shape of it deployed one per app — so teardown reclaims the whole
// worker family, not just the name it can compute. Reclaiming the deterministic
// name is unconditional: it is the project's whether or not the list found it.
func TestPreviewProjectWorkers_ReclaimsTheWholeWorkerFamily(t *testing.T) {
	got := previewProjectWorkers("shop", []string{
		previewWorkerName("shop") + "-web",
		previewWorkerName("shop"),
		previewWorkerName("shop") + "-api",
	})
	want := []string{"ocel-shop--preview", "ocel-shop--preview-api", "ocel-shop--preview-web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("workers = %v, want %v", got, want)
	}
}

func TestPreviewProjectWorkers_AnEmptyListStillReclaimsTheEntrypoint(t *testing.T) {
	got := previewProjectWorkers("shop", nil)
	if !reflect.DeepEqual(got, []string{"ocel-shop--preview"}) {
		t.Errorf("workers = %v, want just the entrypoint worker", got)
	}
}

// Teardown deletes what it is handed, so a name outside the family must never
// reach it — whatever the edge happened to report.
func TestPreviewProjectWorkers_NeverAdoptsANameOutsideTheFamily(t *testing.T) {
	got := previewProjectWorkers("shop", []string{
		previewWorkerName("shopfoo"),
		previewWorkerName("other") + "-web",
		"ocel-shop--preview-web",
		"ocel-shop--previewer",
		"ocel-shop-preview--prod-web",
		workerScriptName("shop", "prod", "web"),
	})
	want := []string{"ocel-shop--preview", "ocel-shop--preview-web"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("workers = %v, want %v", got, want)
	}
}

func TestPreviewProjectWorkers_NoSlugNamesNothing(t *testing.T) {
	if got := previewProjectWorkers("", []string{"ocel-shop--preview"}); got != nil {
		t.Errorf("workers = %v, want none for a project with no slug", got)
	}
}

// Every pointer is served through the project's one wildcard route, so retiring
// one is pure store work — and stays pure store work even when it was the last
// pointer the store held: another deploy may be landing its own pointer right
// now, and it is served by the same worker.
func TestRemovePreview_TouchesNoProjectLevelEdgeState(t *testing.T) {
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
}

// A project holding no root-stack state has no store to ask — and nothing
// project-level to take either: the worker its half-finished deploy left behind
// is the same worker its next deploy reconciles.
func TestRemovePreview_NoRootStackStateTouchesNothing(t *testing.T) {
	fake := &recordingRootStack{}

	if err := RemovePreview(context.Background(), fake, nil, Config{}, "shop", "pr-1", false, nil, nil); err != nil {
		t.Fatalf("RemovePreview: %v", err)
	}
	if fake.destroyed != 0 || len(fake.removedPointers) != 0 || fake.destroyedInstance != 0 {
		t.Errorf("RemovePreview touched the edge with no root-stack state: %+v", fake)
	}
}

// A store that refuses the removal is reported rather than stepped over: the
// pointer is still live and still resolving.
func TestRemovePreview_ReportsAFailedPointerRemoval(t *testing.T) {
	fake := &recordingRootStack{}
	// A state the fake cannot authenticate makes its RemovePointer reject.
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
}

func TestClassifyPreviewStacks_ExcludesProductionAndSiblings(t *testing.T) {
	got := classifyPreviewStacks("shop", []string{
		InfraStackName("shop"),
		AppDeployStackName("shop", "web", buildOnly("b1")),
		"shopfoo--preview-x--web--b1", // sibling project whose id has ours as a prefix
	})
	if len(got.InfraStacks) != 0 || len(got.AppStacks) != 0 || len(got.Pointers) != 0 {
		t.Errorf("classifyPreviewStacks matched non-preview / sibling stacks: %+v", got)
	}
}
