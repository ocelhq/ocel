package readiness

import (
	"maps"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestAPreflightNamesTheAppServingEachDomainAnAppDeclares(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{
		Domains: project.Domains{Production: []string{"acme.com"}},
		Apps: []project.App{
			{Name: "web"},
			{Name: "api", ProductionDomains: []string{"api.acme.com"}},
		},
	}
	sent := newPreflightRequest(cfg, Request{Tier: environmentv1.Tier_TIER_PRODUCTION, Domains: cfg.HostnameNames(environmentv1.Tier_TIER_PRODUCTION)})

	if want := map[string]string{"api.acme.com": "api"}; !maps.Equal(sent.GetDomainApps(), want) {
		t.Errorf("preflight names the apps %v serving its domains, want %v: the hostnames forwarded to an origin are checked before provisioning, and which origin a hostname reaches depends on the app serving it", sent.GetDomainApps(), want)
	}
}

func TestAPreflightAsksForTheForwardingCheckOnlyWhenTheCommandDoes(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{}
	if !newPreflightRequest(cfg, Request{CheckForwarding: true}).GetCheckForwarding() {
		t.Error("a command that asks for the forwarding check sent a preflight that does not")
	}
	if newPreflightRequest(cfg, Request{}).GetCheckForwarding() {
		t.Error("a command that does not ask for the forwarding check sent a preflight that does")
	}
}

func TestAFrameworkAppThatFallsBackToAContainerIsPreflightedAsAContainer(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Framework: "next"},
	}}

	t.Run("a provider that runs containers first", func(t *testing.T) {
		t.Parallel()

		resolved, err := cfg.ResolveComputes([]string{"container", "serverless"}, "fake")
		if err != nil {
			t.Fatalf("ResolveComputes: %v", err)
		}
		sent := newPreflightRequest(resolved, Request{Tier: environmentv1.Tier_TIER_PRODUCTION})
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
