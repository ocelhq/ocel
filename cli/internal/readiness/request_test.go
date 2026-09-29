package readiness

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestOnlyContainerAppsAreNamedEachWithTheArchitectureItDeclares(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Compute: "container", Arch: "arm64"},
		{Name: "worker", Compute: "container"},
		{Name: "api", Compute: "serverless", Serverless: &project.Serverless{Framework: "node"}, Arch: "arm64"},
		{Name: "site"},
	}}

	named := containers(cfg)
	if len(named) != 2 {
		t.Fatalf("containers() = %v, want web and worker alone: nothing else has an image to build", named)
	}
	if named[0].GetApp() != "web" || named[0].GetArch() != "arm64" {
		t.Errorf("containers()[0] = %v, want web declaring arm64", named[0])
	}
	if named[1].GetApp() != "worker" || named[1].GetArch() != "" {
		t.Errorf("containers()[1] = %v, want worker declaring nothing, so the provider names what it runs on", named[1])
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

			got := frameworks(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("frameworks() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAnAppThatDeclaresAContainerSendsNoFramework(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Apps: []project.App{
		{Name: "web", Compute: "container", Serverless: &project.Serverless{Framework: "next", Detected: true}},
		{Name: "api", Serverless: &project.Serverless{Framework: "node", Detected: true}},
	}}
	if got := frameworks(cfg); !reflect.DeepEqual(got, []string{"node"}) {
		t.Errorf("frameworks() = %v, want [node]: web runs its container, so no bootstrap feature its framework needs applies to it", got)
	}
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

		resent, differs := resolvedPreflightRequest(cfg, req, "fake", sent, &contractv1.PreflightResponse{Computes: []string{"container", "serverless"}})
		if !differs {
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

		if _, differs := resolvedPreflightRequest(cfg, req, "fake", sent, &contractv1.PreflightResponse{Computes: []string{"serverless", "container"}}); differs {
			t.Error("the preflight is asked again although web resolves serverless and its framework was already named")
		}
	})

	t.Run("credentials the provider refused", func(t *testing.T) {
		t.Parallel()

		resp := &contractv1.PreflightResponse{
			Computes:           []string{"container"},
			CredentialProblems: []*contractv1.CredentialProblem{{Provider: "fake", Message: "could not authenticate"}},
		}
		if _, differs := resolvedPreflightRequest(cfg, req, "fake", sent, resp); differs {
			t.Error("the preflight is asked again although the credentials it runs with were refused")
		}
	})
}
