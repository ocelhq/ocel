package projects

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func askingInvocation() commands.Invocation {
	invocation := clitest.NewInvocation()
	invocation.StdinIsTerminal = func(io.Reader) bool { return true }
	return invocation
}

func slugsOf(requests []*contractv1.ProjectRequest) []string {
	slugs := make([]string, 0, len(requests))
	for _, req := range requests {
		slugs = append(slugs, req.GetSlug())
	}
	return slugs
}

func tiersOf(requests []*contractv1.ProjectRequest) []environmentv1.Tier {
	var tiers []environmentv1.Tier
	for _, req := range requests {
		tier := req.GetEnvironment().GetTier()
		if tier == environmentv1.Tier_TIER_UNSPECIFIED {
			tier = environmentv1.Tier_TIER_PRODUCTION
		}
		tiers = append(tiers, tier)
	}
	return tiers
}

var bothTiers = []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW}

func TestProjectsDestroyTakesBothTiersBehindOneTypedConfirmation(t *testing.T) {
	project := deployedToBothTiers(t)

	got := runDestroyCommand(t, askingInvocation(), project.Root, "test-app\n")
	if got.err != nil {
		t.Fatalf("ocel projects destroy err = %v; stdout=%s", got.err, got.stdout)
	}

	if asked := strings.Count(got.stdout, "Type the project name"); asked != 1 {
		t.Errorf("asked for the project name %d times, want once for both tiers:\n%s", asked, got.stdout)
	}
	for _, want := range []string{
		"✓ test-app: Read where test-app is deployed in ",
		"✓ test-app: Enumerated what to destroy in production in ",
		"✓ test-app: Enumerated what to destroy in any preview in ",
		`This will permanently destroy production project "test-app"`,
		`ENTIRE preview footprint of project "test-app"`,
		"The account-level preview bootstrap is left intact. This cannot be undone.",
		"Destroyed project test-app",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout has no %q:\n%s", want, got.stdout)
		}
	}
	for _, said := range []string{"stored assets", "variable value "} {
		if strings.Contains(got.stdout, said) {
			t.Errorf("stdout says %q, which the plan's variable values and stored objects groups already name:\n%s", said, got.stdout)
		}
	}
	_, removed := removals(t, project)
	if tiers := tiersOf(removed); !slices.Equal(tiers, bothTiers) {
		t.Fatalf("removed tiers = %v, want production then preview", tiers)
	}
	for _, req := range removed {
		if req.GetSlug() != clitest.FixtureSlug || len(req.GetConsented().GetGroups()) == 0 {
			t.Errorf("removal of %q carries %d plan groups, want test-app's removal carrying the plan that tier showed", req.GetSlug(), len(req.GetConsented().GetGroups()))
		}
	}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if left := recordedStacks(t, project, tier, clitest.FixtureSlug); len(left) != 0 {
			t.Errorf("%s still records %v, want every stack destroyed", tier, left)
		}
	}
}

func TestProjectsDestroyTakesNothingWhenTheNameTypedIsNotTheProjects(t *testing.T) {
	project := deployedToBothTiers(t)

	got := runDestroyCommand(t, askingInvocation(), project.Root, "blog\n")
	if got.err != nil {
		t.Fatalf("ocel projects destroy err = %v; stdout=%s", got.err, got.stdout)
	}
	if _, removed := removals(t, project); len(removed) != 0 {
		t.Errorf("the provider was asked for %d removals, want none without the project name", len(removed))
	}
}

func TestProjectsDestroyWithATierTakesOnlyThatTier(t *testing.T) {
	for _, tc := range []struct {
		flag string
		gone environment.Tier
		kept environment.Tier
		want environmentv1.Tier
	}{
		{"production", environment.TierProduction, environment.TierPreview, environmentv1.Tier_TIER_PRODUCTION},
		{"preview", environment.TierPreview, environment.TierProduction, environmentv1.Tier_TIER_PREVIEW},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			project := deployedToBothTiers(t)

			got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "", "--tier", tc.flag, "--yes")
			if got.err != nil {
				t.Fatalf("ocel projects destroy --tier %s err = %v; stdout=%s", tc.flag, got.err, got.stdout)
			}
			planned, removed := removals(t, project)
			if !slices.Equal(tiersOf(planned), []environmentv1.Tier{tc.want}) || !slices.Equal(tiersOf(removed), []environmentv1.Tier{tc.want}) {
				t.Errorf("planned %v and removed %v, want %s alone", tiersOf(planned), tiersOf(removed), tc.want)
			}
			if left := recordedStacks(t, project, tc.gone, clitest.FixtureSlug); len(left) != 0 {
				t.Errorf("%s still records %v, want it destroyed", tc.gone, left)
			}
			if left := recordedStacks(t, project, tc.kept, clitest.FixtureSlug); len(left) == 0 {
				t.Errorf("%s records nothing, want it left standing", tc.kept)
			}
		})
	}
}

func TestProjectsDestroyOfATierTheProjectIsNotInSaysSo(t *testing.T) {
	project := deployedToProductionOnly(t)

	got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "", "--yes")
	if got.err != nil {
		t.Fatalf("ocel projects destroy err = %v; stdout=%s", got.err, got.stdout)
	}
	if !strings.Contains(got.stdout, "nothing of test-app in preview") {
		t.Errorf("stdout = %q, want it to say test-app has nothing in preview", got.stdout)
	}
	if planned, _ := removals(t, project); !slices.Equal(tiersOf(planned), []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION}) {
		t.Errorf("planned %v, want production alone", tiersOf(planned))
	}
}

func TestProjectsDestroyChecksProductionOnceThoughItFirstReadsWhereTheProjectIsDeployed(t *testing.T) {
	project := deployedToProductionOnly(t)

	got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "", "--yes")
	if got.err != nil {
		t.Fatalf("ocel projects destroy err = %v; stdout=%s", got.err, got.stdout)
	}
	for _, once := range []string{"Loaded the provider", "test-app › prod\n"} {
		if n := strings.Count(got.stdout, once); n != 1 {
			t.Errorf("stdout says %q %d times, want once:\n%s", once, n, got.stdout)
		}
	}
	if sent := clitest.RequestsTo[*contractv1.PreflightRequest](t, project.Requests, contractv1connect.ProviderServicePreflightProcedure); len(sent) != 1 {
		t.Errorf("sent %d preflights, want one: production's answers both what the project is deployed in and whether it can be destroyed", len(sent))
	}
	if _, removed := removals(t, project); len(removed) != 1 {
		t.Errorf("removed %d tiers, want production", len(removed))
	}
}

func TestProjectsDestroyRefusesATierThatIsNotOne(t *testing.T) {
	project := deployedToProductionOnly(t)

	got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "", "--tier", "staging", "--yes")
	if got.err == nil || !strings.Contains(got.err.Error(), `"staging"`) {
		t.Errorf("err = %v, want --tier staging refused naming what was typed", got.err)
	}
}

func TestProjectsDestroyDryShowsEveryPlanAndDestroysNothing(t *testing.T) {
	project := deployedToBothTiers(t)

	got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "", "--dry", "--yes")
	if got.err != nil {
		t.Fatalf("ocel projects destroy --dry err = %v; stdout=%s", got.err, got.stdout)
	}
	for _, want := range []string{
		`This will permanently destroy production project "test-app"`,
		`ENTIRE preview footprint of project "test-app"`,
		"Run without --dry to destroy.",
	} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("stdout has no %q:\n%s", want, got.stdout)
		}
	}
	if planned, removed := removals(t, project); !slices.Equal(tiersOf(planned), bothTiers) || len(removed) != 0 {
		t.Errorf("planned %v and removed %d, want both plans and no removal", tiersOf(planned), len(removed))
	}
}

func TestProjectsDestroyRefusesWithoutATerminalOrYes(t *testing.T) {
	project := deployedToBothTiers(t)

	got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "")
	if got.err == nil {
		t.Fatal("ocel projects destroy without a terminal err = nil, want a refusal")
	}
	if !strings.Contains(got.err.Error(), "ocel projects destroy") || !strings.Contains(got.err.Error(), consent.BypassEnv) {
		t.Errorf("err = %v, want the refusal to name the command and %s", got.err, consent.BypassEnv)
	}
	if code := clierror.NewRunError(got.err); code.GetCode() != clierror.CodeConfirmationRequired || code.GetHint() != "--yes" {
		t.Errorf("run error = %v, want confirmation_required with the hint --yes", code)
	}
	if planned, removed := removals(t, project); len(planned)+len(removed) != 0 {
		t.Errorf("planned %d and removed %d, want nothing asked of the provider", len(planned), len(removed))
	}
}

func TestProjectsDestroyHonoursABypassNamingTheProject(t *testing.T) {
	t.Run("the project's name destroys both tiers and says so", func(t *testing.T) {
		project := deployedToBothTiers(t)
		t.Setenv(consent.BypassEnv, clitest.FixtureSlug)

		got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "")
		if got.err != nil {
			t.Fatalf("ocel projects destroy err = %v; stdout=%s", got.err, got.stdout)
		}
		if !strings.Contains(got.stdout, consent.BypassEnv+"=test-app") {
			t.Errorf("stdout = %q, want the run to warn that it destroys without confirmation", got.stdout)
		}
		if _, removed := removals(t, project); !slices.Equal(tiersOf(removed), bothTiers) {
			t.Errorf("removed %v, want both tiers", tiersOf(removed))
		}
	})

	t.Run("another name is refused", func(t *testing.T) {
		project := deployedToBothTiers(t)
		t.Setenv(consent.BypassEnv, "blog")

		got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "")
		if code := clierror.NewRunError(got.err).GetCode(); code != clierror.CodeConfirmationBypassMismatch {
			t.Errorf("err = %v (code %q), want confirmation_bypass_mismatch", got.err, code)
		}
	})
}

func TestProjectsDestroyOfAnotherProjectUsesTheEdgeItRecorded(t *testing.T) {
	project := deployedToBothTiers(t)
	recordBlog(t, project, stackrecords.Project{
		Edge:     &stackrecords.ProjectEdge{Kind: fake.KindDirect, DNSKind: "zone", DNSZone: "acme.com"},
		Registry: &stackrecords.ProjectRegistry{Server: "registry.example.com", Username: "acme-bot"},
	})

	got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "", "blog", "--yes")
	if got.err != nil {
		t.Fatalf("ocel projects destroy blog err = %v; stdout=%s", got.err, got.stdout)
	}

	planned, removed := removals(t, project)
	if len(planned) != 1 || len(removed) != 1 {
		t.Fatalf("planned %d and removed %d, want blog's production alone", len(planned), len(removed))
	}
	for _, req := range []*contractv1.ProjectRequest{planned[0], removed[0]} {
		if req.GetSlug() != "blog" || req.GetEdge().GetKind() != "direct" || req.GetEdge().GetDns().GetKind() != "zone" || req.GetEdge().GetDns().GetZone() != "acme.com" {
			t.Errorf("request removes %q through edge %q, dns %q, zone %q, want blog's removal through the edge it recorded",
				req.GetSlug(), req.GetEdge().GetKind(), req.GetEdge().GetDns().GetKind(), req.GetEdge().GetDns().GetZone())
		}
	}
	if removed[0].GetProjectRegistry() != nil {
		t.Errorf("registry = %q, want none: its password is never recorded", removed[0].GetProjectRegistry().GetServer())
	}
	if !strings.Contains(got.stdout, "registry.example.com") || !strings.Contains(got.stdout, "stay") {
		t.Errorf("stdout = %q, want a warning that the images blog pushed to registry.example.com stay", got.stdout)
	}
	if left := recordedStacks(t, project, environment.TierProduction, "blog"); len(left) != 0 {
		t.Errorf("blog still records %v, want it destroyed", left)
	}
	if left := recordedStacks(t, project, environment.TierProduction, clitest.FixtureSlug); len(left) == 0 {
		t.Error("test-app records nothing, want the project of this directory left standing")
	}
}

func TestProjectsDestroyOfAnotherProjectChecksTheBootstrapItsRecordedEdgeNeeds(t *testing.T) {
	project := deployedToProductionOnly(t)
	clitest.Bootstrap(t, project.Provider, environment.TierProduction, fake.PartCache)
	recordBlog(t, project, stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindDirect}})

	got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "", "blog", "--yes")
	if got.err != nil {
		t.Fatalf("ocel projects destroy blog err = %v, want blog destroyed: the %s part this directory's relay edge needs is no part of blog's direct edge; stdout=%s", got.err, fake.PartImages, got.stdout)
	}
	if _, removed := removals(t, project); len(removed) != 1 || removed[0].GetSlug() != "blog" {
		t.Errorf("removed %v, want blog's production alone", slugsOf(removed))
	}
	if !strings.Contains(got.stdout, "blog › prod\n") {
		t.Errorf("stdout = %q, want production checked as blog's", got.stdout)
	}
}

func TestProjectsDestroyReadsTheRecordOfNoOtherProject(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		slug string
	}{
		{"this directory's project", nil, clitest.FixtureSlug},
		{"another project", []string{"blog"}, "blog"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := deployedToBothTiers(t)
			recordBlog(t, project, stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindDirect}})
			unreadable := records.Entry{Key: stackrecords.ProjectKey(environment.TierProduction, "docs"), Value: []byte(`{"parts":"cache"}`)}
			if _, err := project.Provider.KeyValues().Write(context.Background(), unreadable); err != nil {
				t.Fatalf("record docs: %v", err)
			}

			got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "", append(tc.args, "--yes")...)
			if got.err != nil {
				t.Fatalf("ocel projects destroy %v err = %v, want %s destroyed: docs's record is no part of it; stdout=%s", tc.args, got.err, tc.slug, got.stdout)
			}
			if _, removed := removals(t, project); len(removed) == 0 || removed[0].GetSlug() != tc.slug {
				t.Errorf("removed %v, want %s's removal", slugsOf(removed), tc.slug)
			}
		})
	}
}

const registryConfig = `
export default {
  slug: "test-app",
  provider: { fake: {} },
  registry: { server: "registry.example.com", username: "acme-bot", password: "${OCEL_TEST_REGISTRY_TOKEN}" },
};
`

func TestProjectsDestroySendsTheRegistryThisDirectoryNamesSoTheImagesItPushedGoWithIt(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	project := deployedToBothTiers(t)
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), registryConfig)

	got := runDestroyCommand(t, clitest.NewInvocation(), project.Root, "", "--yes")
	if got.err != nil {
		t.Fatalf("ocel projects destroy err = %v; stdout=%s", got.err, got.stdout)
	}
	_, removed := removals(t, project)
	if len(removed) != 2 {
		t.Fatalf("removed %d tiers, want both", len(removed))
	}
	for _, req := range removed {
		if registry := req.GetProjectRegistry(); registry.GetServer() != "registry.example.com" || registry.GetPassword() != "hunter2" {
			t.Errorf("registry = %q with its secret resolved %t, want the project's registry with its secret resolved", registry.GetServer(), registry.GetPassword() == "hunter2")
		}
	}
}

func TestProjectsDestroyFromALinkedTreeRecordsADestroyedEventPerTier(t *testing.T) {
	project := deployedToBothTiers(t)
	console := clitest.ServeConsole(t)
	console.Link(t, project.Root)
	invocation := clitest.NewInvocation()
	invocation.Console = clitest.SignedInTo(console.URL)

	got := runDestroyCommand(t, invocation, project.Root, "", "--yes")
	if got.err != nil {
		t.Fatalf("ocel projects destroy err = %v; stdout=%s", got.err, got.stdout)
	}

	var tiers []environmentv1.Tier
	for _, event := range console.Events() {
		if event.GetEvent().GetKind() != consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_DESTROYED {
			t.Errorf("event = %v, want a destroyed event", event)
		}
		tiers = append(tiers, event.GetEvent().GetEnvironment().GetTier())
	}
	if !slices.Equal(tiers, bothTiers) {
		t.Errorf("destroyed events in %v, want one in production and one in preview", tiers)
	}
}

func TestProjectsDestroyOfAnotherProjectRecordsNoConsoleEvent(t *testing.T) {
	project := deployedToBothTiers(t)
	recordBlog(t, project, stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindDirect}})
	console := clitest.ServeConsole(t)
	console.Link(t, project.Root)
	invocation := clitest.NewInvocation()
	invocation.Console = clitest.SignedInTo(console.URL)

	got := runDestroyCommand(t, invocation, project.Root, "", "blog", "--yes")
	if got.err != nil {
		t.Fatalf("ocel projects destroy blog err = %v; stdout=%s", got.err, got.stdout)
	}
	if events := console.Events(); len(events) != 0 {
		t.Errorf("the console received %v, want nothing: this directory links test-app, not blog", events)
	}
}
