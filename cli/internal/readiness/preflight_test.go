package readiness

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func isAskedAgainOnResolving(t *testing.T, cfg *project.Project, req Request, computes ...string) bool {
	t.Helper()
	resolved, err := cfg.ResolveComputes(computes, "fake")
	if err != nil {
		t.Fatalf("ResolveComputes: %v", err)
	}
	return !isAskingTheSame(newPreflightRequest(cfg, req), newPreflightRequest(resolved, req))
}

func TestAFrameworkAppThatFallsBackToAContainerIsAskedAboutAgainWithoutItsFramework(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Serverless: &project.Serverless{Framework: "next", Detected: true}},
	}}
	req := Request{Tier: environmentv1.Tier_TIER_PRODUCTION}
	sent := newPreflightRequest(cfg, req)
	if !reflect.DeepEqual(sent.GetFrameworks(), []string{"next"}) {
		t.Fatalf("first preflight names frameworks %v, want [next]: nothing yet says web cannot run serverless", sent.GetFrameworks())
	}

	t.Run("a provider that runs containers first", func(t *testing.T) {
		t.Parallel()

		resolved, err := cfg.ResolveComputes([]string{"container", "serverless"}, "fake")
		if err != nil {
			t.Fatalf("ResolveComputes: %v", err)
		}
		resent := newPreflightRequest(resolved, req)
		if isAskingTheSame(sent, resent) {
			t.Fatal("the preflight is not asked again, so the bootstrap it reports still requires what next needs for an app that runs a container")
		}
		if len(resent.GetFrameworks()) != 0 {
			t.Errorf("second preflight names frameworks %v, want none", resent.GetFrameworks())
		}
		if named := resent.GetContainers(); len(named) != 1 || named[0].GetApp() != "web" {
			t.Errorf("second preflight names containers %v, want web, so its architecture is read in the same call", named)
		}
	})

	t.Run("a provider that runs serverless first", func(t *testing.T) {
		t.Parallel()

		if isAskedAgainOnResolving(t, cfg, req, "serverless", "container") {
			t.Error("the preflight is asked again although web resolves serverless and its framework was already named")
		}
	})
}

func TestAnAppNamingNoComputeOrFrameworkThatResolvesToAContainerIsAskedAboutAgain(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{{Name: "api", Container: &project.Container{}}}}
	req := Request{Tier: environmentv1.Tier_TIER_PRODUCTION}
	if named := newPreflightRequest(cfg, req).GetContainers(); len(named) != 0 {
		t.Fatalf("first preflight names containers %v, want none: nothing yet says api runs a container", named)
	}
	if !isAskedAgainOnResolving(t, cfg, req, "container") {
		t.Error("the preflight is not asked again, so api's architecture is never read in it: the request names no framework either way, and only its containers differ")
	}
}
