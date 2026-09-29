package preflight

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestHostnames(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{
		Domains: project.Domains{
			Production: []string{"acme.com", "www.acme.com"},
			Preview:    "*.preview.acme.com",
		},
		Apps: []project.App{
			{Name: "web", ProductionDomains: []string{"app.acme.com", "acme.com"}},
			{Name: "api", ProductionDomains: []string{"api.acme.com"}},
			{Name: "admin"},
		},
	}

	t.Run("the project's and the apps' hostnames come back in declared order, deduped", func(t *testing.T) {
		t.Parallel()

		got := Names(Hostnames(cfg, environmentv1.Tier_TIER_PRODUCTION))
		want := []string{"acme.com", "www.acme.com", "app.acme.com", "api.acme.com"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("Hostnames(production) = %v, want %v (declared order, deduped)", got, want)
		}
	})

	t.Run("an environment sees only its own hostnames", func(t *testing.T) {
		t.Parallel()

		if got := Names(Hostnames(cfg, environmentv1.Tier_TIER_PREVIEW)); len(got) != 1 || got[0] != "*.preview.acme.com" {
			t.Errorf("Hostnames(preview) = %v, want [*.preview.acme.com]", got)
		}
	})

	t.Run("a domain-less project declares nothing", func(t *testing.T) {
		t.Parallel()

		if got := Hostnames(&project.Project{}, environmentv1.Tier_TIER_PRODUCTION); len(got) != 0 {
			t.Errorf("Hostnames of a domain-less project = %v, want none", got)
		}
	})

	t.Run("a hostname declared under an app names that app, and a project-wide one names none", func(t *testing.T) {
		t.Parallel()

		want := map[string]string{
			"acme.com":     "",
			"www.acme.com": "",
			"app.acme.com": "web",
			"api.acme.com": "api",
		}
		got := map[string]string{}
		for _, host := range Hostnames(cfg, environmentv1.Tier_TIER_PRODUCTION) {
			got[host.Name] = host.App
		}
		if !maps.Equal(got, want) {
			t.Errorf("production hostnames are attributed %v, want %v: a box points one hostname at one app, and a hostname that reaches the edge naming no app is one a multi-app project cannot bind at all", got, want)
		}
	})

	t.Run("a hostname the project declares and an app repeats stays the project's", func(t *testing.T) {
		t.Parallel()

		at := slices.IndexFunc(Hostnames(cfg, environmentv1.Tier_TIER_PRODUCTION), func(host Hostname) bool { return host.Name == "acme.com" })
		if at < 0 {
			t.Fatal("acme.com is not among the production hostnames at all, and the attribution this test is about never arises")
		}
		if app := Hostnames(cfg, environmentv1.Tier_TIER_PRODUCTION)[at].App; app != "" {
			t.Errorf("acme.com is attributed to %q; the project declares it and web repeats it, and the project-wide declaration is the one that was there first", app)
		}
	})
}
