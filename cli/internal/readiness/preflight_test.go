package readiness

import (
	"maps"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestAPreflightNamesTheAppTheDeployServesEachDomainOn(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		cfg  *project.Project
		want map[string]string
	}{
		"a single app serves the project's own domain": {
			cfg: &project.Project{
				Domains: project.Domains{Production: []string{"acme.com"}},
				Apps:    []project.App{{Name: "go", Compute: "container"}},
			},
			want: map[string]string{"acme.com": "go"},
		},
		"the app the manifest lists first serves the project's own domain, and each app its own": {
			cfg: &project.Project{
				Domains: project.Domains{Production: []string{"acme.com"}},
				Apps: []project.App{
					{Name: "web"},
					{Name: "api", ProductionDomains: []string{"api.acme.com"}},
				},
			},
			want: map[string]string{"acme.com": "api", "api.acme.com": "api"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			sent := newPreflightRequest(tc.cfg, Request{Tier: environmentv1.Tier_TIER_PRODUCTION, Domains: tc.cfg.HostnameNames(environmentv1.Tier_TIER_PRODUCTION)})

			if !maps.Equal(sent.GetDomainApps(), tc.want) {
				t.Errorf("preflight names the apps %v serving its domains, want %v: which origin a hostname reaches depends on the app the deploy serves it on, so the check before provisioning asks about the hostnames the deploy forwards", sent.GetDomainApps(), tc.want)
			}
		})
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

func TestANextContainerBuiltFromItsFunctionIsPreflightedAsAContainer(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Framework: "next", Compute: "container", Arch: "arm64", Container: &project.Container{FromFunction: true}},
	}}
	named := newPreflightRequest(cfg, Request{Tier: environmentv1.Tier_TIER_PRODUCTION}).GetContainers()
	if len(named) != 1 || named[0].GetApp() != "web" || named[0].GetArch() != "arm64" {
		t.Errorf("preflight names containers %v, want web as it declares itself, so its function is built for the architecture its image would be", named)
	}
}
