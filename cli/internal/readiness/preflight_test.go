package readiness

import (
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestAFrameworkAppThatFallsBackToAContainerIsPreflightedAsAContainer(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Serverless: &project.Serverless{Framework: "next", Detected: true}},
	}}

	t.Run("a provider that runs containers first", func(t *testing.T) {
		t.Parallel()

		resolved, err := cfg.ResolveComputes([]string{"container", "serverless"}, "fake")
		if err != nil {
			t.Fatalf("ResolveComputes: %v", err)
		}
		sent := newPreflightRequest(resolved, Request{Tier: environmentv1.Tier_TIER_PRODUCTION})
		if len(sent.GetFrameworks()) != 0 {
			t.Errorf("preflight names frameworks %v, want none: web runs a container", sent.GetFrameworks())
		}
		if named := sent.GetContainers(); len(named) != 1 || named[0].GetApp() != "web" {
			t.Errorf("preflight names containers %v, want web, so its architecture is read in the same call", named)
		}
	})

	t.Run("a provider that runs serverless first", func(t *testing.T) {
		t.Parallel()

		resolved, err := cfg.ResolveComputes([]string{"serverless", "container"}, "fake")
		if err != nil {
			t.Fatalf("ResolveComputes: %v", err)
		}
		sent := newPreflightRequest(resolved, Request{Tier: environmentv1.Tier_TIER_PRODUCTION})
		if got := sent.GetFrameworks(); len(got) != 1 || got[0] != "next" {
			t.Errorf("preflight names frameworks %v, want [next]", got)
		}
		if named := sent.GetContainers(); len(named) != 0 {
			t.Errorf("preflight names containers %v, want none: web runs serverless", named)
		}
	})
}

func TestAnAppNamingNoComputeOrFrameworkThatResolvesToAContainerIsPreflightedAsAContainer(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{{Name: "api", Container: &project.Container{}}}}
	resolved, err := cfg.ResolveComputes([]string{"container"}, "fake")
	if err != nil {
		t.Fatalf("ResolveComputes: %v", err)
	}
	if named := newPreflightRequest(resolved, Request{Tier: environmentv1.Tier_TIER_PRODUCTION}).GetContainers(); len(named) != 1 || named[0].GetApp() != "api" {
		t.Errorf("preflight names containers %v, want api, so its architecture is read in the one call", named)
	}
}
