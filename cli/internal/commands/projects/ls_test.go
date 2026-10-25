package projects

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestProjectsListsEveryProjectWithItsProductionPreviewsEdgeAndLastDeploy(t *testing.T) {
	project := deployedToBothTiers(t)
	recordBlog(t, project, stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindDirect}})

	got := runProjects(t, clitest.NewInvocation(), project.Root, strings.NewReader(""))
	if got.err != nil {
		t.Fatalf("ocel projects err = %v; run=%s", got.err, got.run)
	}

	lines := strings.Split(got.stdout, "\n")
	if !strings.HasPrefix(lines[0], "fake 000000000000") {
		t.Errorf("header = %q, want the provider's identity", lines[0])
	}
	if row, want := rowOf(t, got.stdout, "PROJECT"), []string{"PROJECT", "PRODUCTION", "PREVIEWS", "EDGE", "LAST", "DEPLOY"}; !slices.Equal(row, want) {
		t.Errorf("table header = %v, want %v", row, want)
	}
	if row, want := rowOf(t, got.stdout, "test-app"), []string{"test-app", "●", "live", "2", "relay", "2026-10-05"}; !slices.Equal(row, want) {
		t.Errorf("test-app row = %v, want %v", row, want)
	}
	if row, want := rowOf(t, got.stdout, "blog"), []string{"blog", "○", "none", "—", "direct", "2026-09-14"}; !slices.Equal(row, want) {
		t.Errorf("blog row = %v, want %v", row, want)
	}
	if strings.Index(got.stdout, "  blog") > strings.Index(got.stdout, "  test-app") {
		t.Errorf("output = %q, want the projects in slug order", got.stdout)
	}
	if last := lines[len(lines)-2]; last != "2 projects" {
		t.Errorf("last line = %q, want the count of projects", last)
	}
}

func TestProjectsOnATerminalColoursALiveProduction(t *testing.T) {
	project := deployedToBothTiers(t)
	invocation := clitest.NewInvocation()
	onATerminal(&invocation)

	got := runProjects(t, invocation, project.Root, strings.NewReader(""))
	if got.err != nil {
		t.Fatalf("ocel projects err = %v; run=%s", got.err, got.run)
	}
	if want := "\x1b[32m● live\x1b[0m"; !strings.Contains(got.stdout, want) {
		t.Errorf("output = %q, want live production in green", got.stdout)
	}
}

func TestProjectsSaysWhenTheAccountHasNone(t *testing.T) {
	project := clitest.SetUpProject(t)

	got := runProjects(t, clitest.NewInvocation(), project.Root, strings.NewReader(""))
	if got.err != nil {
		t.Fatalf("ocel projects err = %v; run=%s", got.err, got.run)
	}
	if lines := strings.Split(strings.TrimSpace(got.stdout), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "no projects") {
		t.Errorf("stdout = %q, want one line saying the account has no projects", got.stdout)
	}
}

func TestProjectsAsJSONIsAProjectListResult(t *testing.T) {
	project := deployedToBothTiers(t)
	recordBlog(t, project, stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindDirect}})
	invocation := clitest.NewInvocation()
	asJSON(&invocation)

	got := runProjects(t, invocation, project.Root, strings.NewReader(""))
	if got.err != nil {
		t.Fatalf("ocel projects --json err = %v; run=%s", got.err, got.run)
	}

	var result resultv1.ProjectListResult
	clitest.DecodeResultInto(t, got.stdout, &result)
	if len(result.GetProjects()) != 2 {
		t.Fatalf("projects = %v, want blog and test-app", result.GetProjects())
	}
	blog, app := result.GetProjects()[0], result.GetProjects()[1]
	if blog.GetSlug() != "blog" || blog.GetEdge() != "direct" || blog.GetProductionPromotionId() != "" || blog.GetLastDeployedAt() != "2026-09-14T09:00:00Z" ||
		!slices.Equal(blog.GetTiers(), []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION}) {
		t.Errorf("blog = %v, want blog in production on the direct edge, last deployed 2026-09-14T09:00:00Z", blog)
	}
	if app.GetSlug() != "test-app" || app.GetEdge() != "relay" || app.GetProductionPromotionId() != promotionID || app.GetPreviews() != 2 || app.GetLastDeployedAt() != "2026-10-05T09:00:00Z" ||
		!slices.Equal(app.GetTiers(), []environmentv1.Tier{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW}) {
		t.Errorf("test-app = %v, want it in both tiers on the relay edge, live as %s with 2 previews", app, promotionID)
	}
}

func TestProjectsLsListsWhatProjectsLists(t *testing.T) {
	project := deployedToBothTiers(t)
	recordBlog(t, project, stackrecords.Project{Edge: &stackrecords.ProjectEdge{Kind: fake.KindDirect}})

	bare := runProjects(t, clitest.NewInvocation(), project.Root, strings.NewReader(""))
	ls := runProjects(t, clitest.NewInvocation(), project.Root, strings.NewReader(""), "ls")
	if bare.err != nil || ls.err != nil {
		t.Fatalf("ocel projects err = %v, ocel projects ls err = %v", bare.err, ls.err)
	}
	if bare.stdout != ls.stdout || !strings.Contains(ls.stdout, "test-app") {
		t.Errorf("ocel projects ls = %q, want what ocel projects lists: %q", ls.stdout, bare.stdout)
	}
}
