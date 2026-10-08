package promotions

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/pkg/environment"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const fixtureTarget = "fake/box"

func rollBack(t *testing.T, project clitest.FakeProject, console *clitest.FakeConsole, opts rollbackOptions) (stderr string, err error) {
	t.Helper()
	project.Provider.FakeConnector().Runs(provider.ConnectorTarget{Fingerprint: fixtureTarget})
	invocation := clitest.NewInvocation()
	invocation.Console = clitest.SignedInTo(console.URL)
	var out, errOut bytes.Buffer
	clitest.AttachTerminalSink(invocation, &out)
	opts.yes = true
	err = runRollback(context.Background(), invocation, project.Root, opts, &out, &errOut, strings.NewReader(""))
	return errOut.String(), err
}

func withWebApp(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  apps: [{ name: "web", path: "apps/web", framework: "node" }],
};
`)
	clitest.WriteFile(t, filepath.Join(project.Root, "apps", "web", "src", "server.ts"), "export function handler() { return \"web\"; }\n")
}

func TestARollbackFromALinkedTreeReportsOneRollbackDeploymentToTheConsole(t *testing.T) {
	project := promotedTwice(t)
	withWebApp(t, project)
	console := clitest.ServeConsole(t)
	console.Link(t, project.Root)

	stderr, err := rollBack(t, project, console, rollbackOptions{})
	if err != nil {
		t.Fatalf("runRollback err = %v; stderr=%s", err, stderr)
	}

	reports := console.Reports()
	if len(reports) != 1 {
		t.Fatalf("the console received %d reports, want 1; stderr=%s", len(reports), stderr)
	}
	got := reports[0].GetDeployment()
	if got.GetKind() != consolev1.DeploymentKind_DEPLOYMENT_KIND_ROLLBACK || got.GetOutcome() != consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED {
		t.Errorf("deployment is a %v %v, want a succeeded rollback", got.GetKind(), got.GetOutcome())
	}
	if want := activePromotionID(t, project); got.GetPromotion().GetId() != want {
		t.Errorf("promotion = %q, want the new %q production serves", got.GetPromotion().GetId(), want)
	}
	if len(got.GetApps()) != 1 || got.GetApps()[0].GetName() != "web" || got.GetApps()[0].GetRelease() != "build-1~fp1" || got.GetApps()[0].GetBuildId() != "build-1" {
		t.Errorf("apps = %v, want web at the release promo-1 held", got.GetApps())
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing from a report that went through", stderr)
	}
	if file := readReportFile(t, project.Root); file.GetId() != got.GetId() {
		t.Errorf("the report file holds deployment %q, want the %q the console received", file.GetId(), got.GetId())
	}
}

func TestARolledBackPromotionIsSequencedByTheTimeTheRouterRecordedIt(t *testing.T) {
	project := promotedTwice(t)
	withWebApp(t, project)
	console := clitest.ServeConsole(t)
	console.Link(t, project.Root)

	if stderr, err := rollBack(t, project, console, rollbackOptions{}); err != nil {
		t.Fatalf("runRollback err = %v; stderr=%s", err, stderr)
	}

	active, _, err := project.Provider.Releases(environment.TierProduction, clitest.FixtureSlug).ReadActive(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := console.Reports()[0].GetDeployment().GetPromotion().GetSeq(); got != active.Ts {
		t.Errorf("promotion seq = %d, want the router's ts %d", got, active.Ts)
	}
}

func TestARollbackFromAnUnlinkedTreeMakesNoCallAndPrintsOneHintNamingOcelLink(t *testing.T) {
	project := promotedTwice(t)
	console := clitest.ServeConsole(t)

	stderr, err := rollBack(t, project, console, rollbackOptions{})
	if err != nil {
		t.Fatalf("runRollback err = %v; stderr=%s", err, stderr)
	}

	if len(console.Reports()) != 0 {
		t.Errorf("the console received %d reports from an unlinked tree, want none", len(console.Reports()))
	}
	if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "`ocel link`") {
		t.Errorf("stderr = %q, want one hint line naming `ocel link`", stderr)
	}
}

func TestARollbackWhoseConsoleIsUnreachableStillSucceedsWithOneWarning(t *testing.T) {
	project := promotedTwice(t)
	down := httptest.NewServer(http.NotFoundHandler())
	apiURL := down.URL
	down.Close()
	clitest.LinkToConsole(t, project.Root, apiURL)
	console := &clitest.FakeConsole{URL: apiURL}

	stderr, err := rollBack(t, project, console, rollbackOptions{})
	if err != nil {
		t.Fatalf("runRollback err = %v, want a rollback to succeed whatever the console does", err)
	}

	if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "Couldn't report deployment") {
		t.Errorf("stderr = %q, want one warning line naming the deployment", stderr)
	}
}

func TestADryRollbackReportsNothing(t *testing.T) {
	project := promotedTwice(t)
	console := clitest.ServeConsole(t)
	console.Link(t, project.Root)

	if stderr, err := rollBack(t, project, console, rollbackOptions{dry: true}); err != nil {
		t.Fatalf("runRollback err = %v; stderr=%s", err, stderr)
	}

	if len(console.Reports()) != 0 {
		t.Errorf("the console received %d reports from a dry run, want none", len(console.Reports()))
	}
}

func readReportFile(t *testing.T, root string) *consolev1.Deployment {
	t.Helper()
	return clitest.ReadDeployReport(t, deployreport.Path(root))
}
