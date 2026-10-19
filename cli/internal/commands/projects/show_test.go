package projects

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/environment"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

const webRelease = "00000000000000000000000000000001~000000000001"

func TestProjectsShowListsEachEnvironmentWithItsPromotionAppsAndURL(t *testing.T) {
	project := deployedToBothTiers(t)
	serveOnProductionHost(t, project)

	got := runProjects(t, clitest.NewInvocation(), project.Root, strings.NewReader(""), "show")
	if got.err != nil {
		t.Fatalf("ocel projects show err = %v; run=%s", got.err, got.run)
	}

	header := strings.Split(got.stdout, "\n")[0]
	if !strings.HasPrefix(header, "test-app · fake 000000000000") || !strings.HasSuffix(header, " · edge relay") {
		t.Errorf("header = %q, want the project, the provider's identity and its edge", header)
	}
	for _, column := range []string{"ENVIRONMENT", "TIER", "PROMOTION", "APPS", "URL"} {
		if !strings.Contains(got.stdout, column) {
			t.Errorf("output has no %s column:\n%s", column, got.stdout)
		}
	}
	if row, want := rowOf(t, got.stdout, "prod"), []string{"prod", "production", promotionID, "web", webRelease, "https://" + productionHost}; !slices.Equal(row, want) {
		t.Errorf("prod row = %v, want %v: the env is named by its slug, its tier by the word", row, want)
	}
	if row, want := rowOf(t, got.stdout, "pr-1"), []string{"pr-1", "preview", "—", "—", "—"}; !slices.Equal(row, want) {
		t.Errorf("pr-1 row = %v, want %v", row, want)
	}
	rowOf(t, got.stdout, "pr-2")
}

func TestProjectsShowOfAnotherProjectNamesTheEdgeItRecorded(t *testing.T) {
	project := deployedToBothTiers(t)
	recordBlog(t, project, stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindDirect}})

	got := runProjects(t, clitest.NewInvocation(), project.Root, strings.NewReader(""), "show", "blog")
	if got.err != nil {
		t.Fatalf("ocel projects show blog err = %v; run=%s", got.err, got.run)
	}

	if header := strings.Split(got.stdout, "\n")[0]; !strings.HasPrefix(header, "blog · ") || !strings.HasSuffix(header, " · edge direct") {
		t.Errorf("header = %q, want blog on the edge it recorded", header)
	}
}

func TestProjectsShowMarksAResourceThatHasAConsoleLink(t *testing.T) {
	t.Run("an arrow marks the id", func(t *testing.T) {
		project := deployedToProductionOnly(t)
		recordWebFunction(t, project)

		got := runProjects(t, clitest.NewInvocation(), project.Root, strings.NewReader(""), "show")
		if got.err != nil {
			t.Fatalf("ocel projects show err = %v; run=%s", got.err, got.run)
		}
		for _, want := range []string{"RESOURCES", "STACK", "KIND", "ID", "fake:function", webFunctionID + " ↗"} {
			if !strings.Contains(got.stdout, want) {
				t.Errorf("output has no %q:\n%s", want, got.stdout)
			}
		}
		if strings.Contains(got.stdout, "\x1b]8;;") {
			t.Errorf("output = %q, want no hyperlink off a terminal", got.stdout)
		}
	})

	t.Run("a terminal links the id to the console", func(t *testing.T) {
		project := deployedToProductionOnly(t)
		recordWebFunction(t, project)
		invocation := clitest.NewInvocation()
		onATerminal(&invocation)

		got := runProjects(t, invocation, project.Root, strings.NewReader(""), "show")
		if got.err != nil {
			t.Fatalf("ocel projects show err = %v; run=%s", got.err, got.run)
		}
		if want := "\x1b]8;;" + webFunctionLink + "\x1b\\" + webFunctionID + "\x1b]8;;\x1b\\ ↗"; !strings.Contains(got.stdout, want) {
			t.Errorf("output = %q, want the id linked to its console page", got.stdout)
		}
	})
}

func TestProjectsShowWithLinksPrintsEachConsoleURL(t *testing.T) {
	project := deployedToProductionOnly(t)
	recordWebFunction(t, project)
	invocation := clitest.NewInvocation()
	onATerminal(&invocation)

	got := runProjects(t, invocation, project.Root, strings.NewReader(""), "show", "--links")
	if got.err != nil {
		t.Fatalf("ocel projects show --links err = %v; run=%s", got.err, got.run)
	}
	row := rowOf(t, got.stdout, webStackName(t, project))
	if !slices.Contains(row, webFunctionLink) {
		t.Errorf("row = %v, want the console URL printed in its own column", row)
	}
	if strings.Contains(got.stdout, "\x1b]8;;") {
		t.Errorf("output = %q, want --links to print URLs instead of hyperlinks", got.stdout)
	}
}

func webStackName(t *testing.T, project clitest.FakeProject) string {
	t.Helper()
	for _, stack := range recordedStacks(t, project, environment.TierProduction, clitest.FixtureSlug) {
		if stack.App == "web" {
			return stack.Name.String()
		}
	}
	t.Fatal("the production web stack is not recorded")
	return ""
}

func TestProjectsShowAsJSONCarriesEveryConsoleURL(t *testing.T) {
	project := deployedToProductionOnly(t)
	recordWebFunction(t, project)
	invocation := clitest.NewInvocation()
	asJSON(&invocation)

	got := runProjects(t, invocation, project.Root, strings.NewReader(""), "show")
	if got.err != nil {
		t.Fatalf("ocel projects show --json err = %v; run=%s", got.err, got.run)
	}

	var result resultv1.ProjectShowResult
	clitest.DecodeResultInto(t, got.stdout, &result)
	if result.GetSlug() != clitest.FixtureSlug || result.GetEdge() != "relay" {
		t.Errorf("result = %v, want test-app on the relay edge", result.GetSlug())
	}
	var links []string
	for _, group := range result.GetResources() {
		for _, resource := range group.GetResources() {
			if resource.GetConsoleUrl() != "" {
				links = append(links, resource.GetConsoleUrl())
			}
		}
	}
	if !slices.Equal(links, []string{webFunctionLink}) {
		t.Errorf("console URLs = %v, want %s", links, webFunctionLink)
	}
	if len(result.GetEnvironments()) != 1 || result.GetEnvironments()[0].GetTier().String() != "TIER_PRODUCTION" {
		t.Errorf("environments = %v, want production", result.GetEnvironments())
	}
}

func TestProjectsShowNamesThePartsTheProjectShares(t *testing.T) {
	project := deployedToProductionOnly(t)
	project.Provider.FakeBootstrap().AddPart(environment.TierProduction, provider.BootstrapPart{
		Name:      "containers",
		UsedBy:    []string{"blog", clitest.FixtureSlug, "shop"},
		Resources: []provider.PhysicalResource{{Kind: "Fake::Cluster", ID: "ocel-containers-production"}},
	})

	got := runProjects(t, clitest.NewInvocation(), project.Root, strings.NewReader(""), "show")
	if got.err != nil {
		t.Fatalf("ocel projects show err = %v; run=%s", got.err, got.run)
	}

	for _, want := range []string{"SHARED", "test-app shares containers with 2 other projects", "Fake::Cluster", "ocel-containers-production"} {
		if !strings.Contains(got.stdout, want) {
			t.Errorf("output has no %q:\n%s", want, got.stdout)
		}
	}
}
