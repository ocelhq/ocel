package deploy

import (
	"context"
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cloud/edge"
)

func TestRootStackWorkerNames_CoversEveryAppFromHistoryNotTheSharedStore(t *testing.T) {
	f := &recordingRootStack{
		history: []edge.HistoryEntry{
			{Promotion: edge.Promotion{PromotionID: "p2", Builds: map[string]string{"web": "b2", "api": "b2"}}},
			{Promotion: edge.Promotion{PromotionID: "p1", Builds: map[string]string{"web": "b1"}}},
		},
	}
	ctx := context.Background()
	state, err := f.ReconcileRootStack(ctx, edge.RootStackSpec{Version: "v1", Slug: "proj-x"}, nil)
	if err != nil {
		t.Fatalf("ReconcileRootStack: %v", err)
	}

	names, err := rootStackWorkerNames(ctx, f, state, "proj_x", "prod")
	if err != nil {
		t.Fatalf("rootStackWorkerNames: %v", err)
	}

	prod := "proj_x-prod"
	for _, want := range []string{
		legacyWorkerName(prod),
		workerScriptName("proj_x", "prod", "root"),
		workerScriptName("proj_x", "prod", "web"),
		workerScriptName("proj_x", "prod", "api"),
	} {
		if !contains(names, want) {
			t.Errorf("worker names %v missing %q", names, want)
		}
	}

	for _, n := range names {
		if n == "ocel-deployments-store" {
			t.Errorf("worker names %v must not include the shared store worker", names)
		}
	}

	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Errorf("duplicate worker name %q in %v", n, names)
		}
		seen[n] = true
	}
}

func TestRootStackWorkerNames_PropagatesHistoryError(t *testing.T) {
	_, err := rootStackWorkerNames(context.Background(), &recordingRootStack{}, edge.RootStackState{}, "proj_x", "prod")
	if err == nil {
		t.Fatal("rootStackWorkerNames with an unreadable store err = nil, want the history error")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestClassifyProjectStacks_SplitsInfraFromAppStacks(t *testing.T) {
	got := classifyProjectStacks("shop", []string{
		"shop--infra",
		"shop--web--b1",
		"shop--api--b2",
	})
	want := ProjectTeardownPlan{
		InfraStack: "shop--infra",
		AppStacks:  []string{"shop--web--b1", "shop--api--b2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("classifyProjectStacks = %+v, want %+v", got, want)
	}
}

func TestClassifyProjectStacks_IncludesOrphanAppStacks(t *testing.T) {
	got := classifyProjectStacks("shop", []string{"shop--web--b1", "shop--web--aborted"})
	want := []string{"shop--web--b1", "shop--web--aborted"}
	if !reflect.DeepEqual(got.AppStacks, want) {
		t.Fatalf("AppStacks = %v, want %v", got.AppStacks, want)
	}
	if got.InfraStack != "" {
		t.Errorf("InfraStack = %q, want empty", got.InfraStack)
	}
}

func TestClassifyProjectStacks_ExcludesOtherProjectsAndPreviews(t *testing.T) {
	got := classifyProjectStacks("shop", []string{
		"shop--infra",
		"shop--web--b1",
		"shopfoo--infra",
		"shopfoo--web--b1",
		"other--infra",
		"shop-preview-pr1",
	})
	want := ProjectTeardownPlan{
		InfraStack: "shop--infra",
		AppStacks:  []string{"shop--web--b1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("classifyProjectStacks = %+v, want %+v", got, want)
	}
}

func TestClassifyProjectStacks_AppNamedInfraIsNotTheInfraStack(t *testing.T) {
	got := classifyProjectStacks("shop", []string{"shop--infra", "shop--infra--b1"})
	want := ProjectTeardownPlan{
		InfraStack: "shop--infra",
		AppStacks:  []string{"shop--infra--b1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("classifyProjectStacks = %+v, want %+v", got, want)
	}
}

func TestClassifyProjectStacks_NoStacksIsEmptyPlan(t *testing.T) {
	got := classifyProjectStacks("shop", nil)
	if got.InfraStack != "" || len(got.AppStacks) != 0 {
		t.Fatalf("classifyProjectStacks(nil) = %+v, want empty plan", got)
	}
}

func TestProjectPrefixes_TrailingSlashScopesToProject(t *testing.T) {
	if p := projectAssetR2Prefix("shop"); p != "assets/shop/" {
		t.Errorf("projectAssetR2Prefix = %q, want assets/shop/", p)
	}
	if p := projectISRPrefix("prod", "shop"); p != "prod/shop/" {
		t.Errorf("projectISRPrefix = %q, want prod/shop/", p)
	}
	if p := projectEdgeR2Prefix("shop"); p != "edge/shop/" {
		t.Errorf("projectEdgeR2Prefix = %q, want edge/shop/", p)
	}
	if p := projectImageConfigPrefix("shop"); p != "image-config/shop/" {
		t.Errorf("projectImageConfigPrefix = %q, want image-config/shop/", p)
	}
}
