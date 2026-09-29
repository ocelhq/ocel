package preflight

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"google.golang.org/protobuf/proto"
)

func TestOnlyContainerAppsAreNamedEachWithTheArchitectureItDeclares(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Compute: "container", Arch: "arm64"},
		{Name: "worker", Compute: "container"},
		{Name: "api", Compute: "serverless", Serverless: &project.Serverless{Framework: "node"}, Arch: "arm64"},
		{Name: "site"},
	}}

	named := Containers(cfg)
	if len(named) != 2 {
		t.Fatalf("Containers() = %v, want web and worker alone: nothing else has an image to build", named)
	}
	if named[0].GetApp() != "web" || named[0].GetArch() != "arm64" {
		t.Errorf("Containers()[0] = %v, want web declaring arm64", named[0])
	}
	if named[1].GetApp() != "worker" || named[1].GetArch() != "" {
		t.Errorf("Containers()[1] = %v, want worker declaring nothing, so the provider names what it runs on", named[1])
	}
}

func TestTheProjectsRuntimesAreTheFrameworksItsAppsName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  *project.Project
		want []string
	}{
		{
			name: "a project with no apps names no framework",
			cfg:  &project.Project{},
		},
		{
			name: "an app with no framework is left out",
			cfg:  &project.Project{Apps: []project.App{{Name: "web"}}},
		},
		{
			name: "each app's framework is named",
			cfg: &project.Project{Apps: []project.App{
				{Serverless: &project.Serverless{Framework: "next"}},
				{Serverless: &project.Serverless{Framework: "node"}},
			}},
			want: []string{"next", "node"},
		},
		{
			name: "an arch does not split one runtime in two",
			cfg: &project.Project{Apps: []project.App{
				{Serverless: &project.Serverless{Framework: "next"}},
				{Serverless: &project.Serverless{Framework: "next"}, Arch: "x86_64"},
			}},
			want: []string{"next"},
		},
		{
			name: "two apps on one runtime name it once",
			cfg: &project.Project{Apps: []project.App{
				{Serverless: &project.Serverless{Framework: "next"}},
				{Serverless: &project.Serverless{Framework: "next"}},
			}},
			want: []string{"next"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := Frameworks(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Runtimes = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTheHostnamesAProjectDeclaresComeBackInDeclaredOrder(t *testing.T) {
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

func TestTheIdentityEventNamesTheProjectTheTierAndBothParties(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Slug: "acme", Edge: &project.Edge{Kind: "relay"}}

	t.Run("names the project, the tier and both parties", func(t *testing.T) {
		t.Parallel()

		got := IdentityEvent(cfg, environmentv1.Tier_TIER_PRODUCTION, &contractv1.Identity{
			Provider:  "fake",
			Account:   "123456789012",
			Principal: "deploy",
			Location:  "zone-a",
			EdgeScope: "a1b2c3d4",
			Details:   []*contractv1.Detail{{Label: "profile", Value: "default"}},
		})
		want := &streamv1.IdentityEvent{
			Project: "acme",
			Tier:    environmentv1.Tier_TIER_PRODUCTION,
			Origin:  &streamv1.Party{Vendor: "fake", Account: "123456789012", Principal: "deploy", Location: "zone-a"},
			Edge:    &streamv1.Party{Vendor: "relay", Account: "a1b2c3d4"},
		}
		if !proto.Equal(got, want) {
			t.Errorf("IdentityEvent() = %v, want %v", got, want)
		}
	})

	t.Run("no edge scope means no edge party", func(t *testing.T) {
		t.Parallel()

		got := IdentityEvent(cfg, environmentv1.Tier_TIER_PREVIEW, &contractv1.Identity{
			Provider: "fake",
			Account:  "srv1.example.com",
		})
		if got.GetEdge() != nil {
			t.Errorf("edge = %v, want nothing: the provider reported no edge scope", got.GetEdge())
		}
	})

	t.Run("an empty identity has no origin", func(t *testing.T) {
		t.Parallel()

		if got := IdentityEvent(cfg, environmentv1.Tier_TIER_PREVIEW, &contractv1.Identity{}); got.GetOrigin() != nil {
			t.Errorf("origin = %v, want nothing to represent an identity the provider left blank", got.GetOrigin())
		}
	})
}

func TestCredentialProblemsAreNilWhenThereAreNoneAndAggregatedOtherwise(t *testing.T) {
	t.Parallel()

	t.Run("nil when there are none", func(t *testing.T) {
		t.Parallel()

		if err := credentialProblems(nil); err != nil {
			t.Errorf("expected nil error for no problems, got %v", err)
		}
	})

	t.Run("aggregates all of them", func(t *testing.T) {
		t.Parallel()

		err := credentialProblems([]*contractv1.CredentialProblem{
			{Provider: "Fake", Message: "could not authenticate", Hint: "run fake login"},
			{Provider: "Relay", Message: "FAKE_RELAY_TOKEN is not set", Hint: "export it"},
		})
		if err == nil {
			t.Fatal("expected an error aggregating the problems")
		}
		for _, want := range []string{"Fake", "could not authenticate", "run fake login", "Relay", "FAKE_RELAY_TOKEN is not set", "export it"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("aggregated error missing %q:\n%s", want, err.Error())
			}
		}
	})
}

func TestAMatchingInfrastructureTierPassesTheCheck(t *testing.T) {
	cases := []struct {
		infra, required environmentv1.Tier
	}{
		{environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PREVIEW},
		{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PRODUCTION},
	}
	for _, c := range cases {
		if err := checkTier(c.infra, c.required); err != nil {
			t.Errorf("checkTier(%v, %v) = %v, want nil", c.infra, c.required, err)
		}
	}
}

func TestAMismatchedInfrastructureTierFailsTheCheck(t *testing.T) {
	cases := []struct {
		infra, required environmentv1.Tier
	}{
		{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW},
		{environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PRODUCTION},
		{environmentv1.Tier_TIER_UNSPECIFIED, environmentv1.Tier_TIER_PREVIEW},
		{environmentv1.Tier_TIER_UNSPECIFIED, environmentv1.Tier_TIER_PRODUCTION},
	}
	for _, c := range cases {
		err := checkTier(c.infra, c.required)
		if err == nil {
			t.Errorf("checkTier(%v, %v) = nil, want error", c.infra, c.required)
			continue
		}
		if !strings.Contains(err.Error(), "infrastructure") {
			t.Errorf("checkTier(%v, %v) error names no infrastructure: %q", c.infra, c.required, err)
		}
	}
}

func TestCheckTier_ErrorNamesTheInfraAndTheBootstrap(t *testing.T) {
	err := checkTier(environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "preview infrastructure") {
		t.Errorf("error should name preview infrastructure, got %q", msg)
	}
	if !strings.Contains(msg, "ocel bootstrap preview") {
		t.Errorf("error should tell the user how to fix it, got %q", msg)
	}
	if strings.Contains(msg, "ocel preview can only") {
		t.Errorf("error names a command the caller may not have run, got %q", msg)
	}

	err = checkTier(environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PRODUCTION)
	if err == nil {
		t.Fatal("expected error")
	}
	msg = err.Error()
	if !strings.Contains(msg, "production infrastructure") {
		t.Errorf("error should name production infrastructure, got %q", msg)
	}
	if !strings.Contains(msg, "ocel bootstrap production") {
		t.Errorf("error should tell the user how to fix it, got %q", msg)
	}
	if strings.Contains(msg, "ocel deploy can only") {
		t.Errorf("error names a command the caller may not have run, got %q", msg)
	}
}
