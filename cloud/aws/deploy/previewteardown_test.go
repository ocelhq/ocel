package deploy

import (
	"reflect"
	"sort"
	"testing"
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
	targets, err := PreviewReclaimTargets("shop", "pr-1", "preview-pr-1", []string{"record:web/b1", "record:api/b2"})
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
	if got, want := byApp["web"].Stack, PreviewAppDeployStackName("shop", "pr-1", "web", "b1"); got != want {
		t.Errorf("web stack = %q, want the pointer-scoped preview stack %q", got, want)
	}
	// The production reclaim of the same record must resolve a different stack —
	// proving preview and production never collide on stack names.
	prod, _ := ReclaimTargets("shop", "prod", []string{"record:web/b1"})
	if prod[0].Stack == byApp["web"].Stack {
		t.Error("preview and production reclaim resolved the same stack name")
	}
}

func TestReclaimTargetsFor_BranchesOnPointer(t *testing.T) {
	prod, err := reclaimTargetsFor("shop", "", "prod", []string{"record:web/b1"})
	if err != nil {
		t.Fatalf("reclaimTargetsFor(production): %v", err)
	}
	if prod[0].Stack != AppDeployStackName("shop", "web", "b1") {
		t.Errorf("empty pointer must resolve production stacks, got %q", prod[0].Stack)
	}
	preview, err := reclaimTargetsFor("shop", "pr-1", "preview-pr-1", []string{"record:web/b1"})
	if err != nil {
		t.Fatalf("reclaimTargetsFor(preview): %v", err)
	}
	if preview[0].Stack != PreviewAppDeployStackName("shop", "pr-1", "web", "b1") {
		t.Errorf("named pointer must resolve preview stacks, got %q", preview[0].Stack)
	}
}

func TestClassifyPreviewStacks_SplitsInfraAppAndPointers(t *testing.T) {
	got := classifyPreviewStacks("shop", []string{
		PreviewInfraStackName("shop", "staging"),
		PreviewAppDeployStackName("shop", "staging", "web", "b1"),
		PreviewAppDeployStackName("shop", "pr-1", "web", "b2"),
		PreviewAppDeployStackName("shop", "pr-1", "api", "b3"),
		InfraStackName("shop"),                  // production infra — not a preview stack
		AppDeployStackName("shop", "web", "b9"), // production app — not a preview stack
		"other--preview-x--web--b1",             // another project's preview
	})

	sort.Strings(got.InfraStacks)
	sort.Strings(got.AppStacks)
	wantInfra := []string{PreviewInfraStackName("shop", "staging")}
	wantApps := []string{
		PreviewAppDeployStackName("shop", "pr-1", "api", "b3"),
		PreviewAppDeployStackName("shop", "pr-1", "web", "b2"),
		PreviewAppDeployStackName("shop", "staging", "web", "b1"),
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

func TestClassifyPreviewStacks_ExcludesProductionAndSiblings(t *testing.T) {
	got := classifyPreviewStacks("shop", []string{
		InfraStackName("shop"),
		AppDeployStackName("shop", "web", "b1"),
		"shopfoo--preview-x--web--b1", // sibling project whose id has ours as a prefix
	})
	if len(got.InfraStacks) != 0 || len(got.AppStacks) != 0 || len(got.Pointers) != 0 {
		t.Errorf("classifyPreviewStacks matched non-preview / sibling stacks: %+v", got)
	}
}
