package deploy

import (
	"reflect"
	"testing"
)

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
	// An aborted deploy leaves an app stack no promotion ever named; a
	// backend-prefix scan must still collect it, unlike a store-driven prune.
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
		"shopfoo--infra",     // sibling project whose id has ours as a prefix
		"shopfoo--web--b1",   // ditto
		"other--infra",       // unrelated project
		"shop-preview-pr1",   // a preview stack (single-dash), not production
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
	// Only the exact "<project>--infra" name is the infra stack; an app that
	// happens to be named "infra" produces "<project>--infra--<build>", which
	// stays an app stack.
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
}
