package deploy

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/pkg/environment"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

const fixtureTarget = "fake/box"

func reportingDeploy(t *testing.T) (Dependencies, clitest.FakeProject, *clitest.FakeConsole) {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	fixture := setUpDeployProject(t)
	addAppToFixtureConfig(t, fixture.Root)
	writeServeDescriptor(t, fixture.Root, "api", "bld_api_1")
	fixture.Provider.FakeConnector().Runs(provider.ConnectorTarget{Fingerprint: fixtureTarget})
	console := clitest.ServeConsole(t)
	dependencies.Console = clitest.SignedInTo(console.URL)
	return dependencies, fixture, console
}

func deployOnce(t *testing.T, dependencies Dependencies, fixture clitest.FakeProject, opts deployOptions) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)
	opts.yes = true
	err = runDeploy(context.Background(), dependencies, fixture.Root, opts, &out, &errOut, strings.NewReader(""))
	return out.String(), errOut.String(), err
}

func readDeployReport(t *testing.T, root string) *consolev1.Deployment {
	t.Helper()
	raw, err := os.ReadFile(deployreport.Path(root))
	if err != nil {
		t.Fatalf("read the deploy report: %v", err)
	}
	var report consolev1.Deployment
	if err := protojson.Unmarshal(raw, &report); err != nil {
		t.Fatalf("the deploy report is not a deployment's protojson: %v", err)
	}
	return &report
}

func TestADeployFromALinkedTreeReportsOneDeploymentToTheConsole(t *testing.T) {
	dependencies, fixture, console := reportingDeploy(t)
	console.Link(t, fixture.Root)

	stdout, stderr, err := deployOnce(t, dependencies, fixture, deployOptions{tag: "v9"})
	if err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout, stderr)
	}

	reports := console.Reports()
	if len(reports) != 1 {
		t.Fatalf("the console received %d reports, want 1; stderr=%s", len(reports), stderr)
	}
	got := reports[0].GetDeployment()
	if reports[0].GetProjectId() != clitest.FixtureConsoleProjectID {
		t.Errorf("project = %q, want the linked project", reports[0].GetProjectId())
	}
	if got.GetKind() != consolev1.DeploymentKind_DEPLOYMENT_KIND_DEPLOY || got.GetOutcome() != consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED {
		t.Errorf("deployment is %v %v, want a succeeded deploy", got.GetKind(), got.GetOutcome())
	}
	if want := activePromotion(t, fixture, environment.TierProduction, router.DefaultPointer); want == "" || got.GetPromotion().GetId() != want || got.GetPromotion().GetTag() != "v9" {
		t.Errorf("promotion = %v, want the %q production now serves, tagged v9", got.GetPromotion(), want)
	}
	if got.GetTarget() != fixtureTarget || got.GetProvider().GetName() != "fake" {
		t.Errorf("target %q provider %q, want %q and fake", got.GetTarget(), got.GetProvider().GetName(), fixtureTarget)
	}
	if len(got.GetApps()) != 1 || got.GetApps()[0].GetName() != "api" || got.GetApps()[0].GetRelease() == "" {
		t.Errorf("apps = %v, want api at the release the provider made live", got.GetApps())
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing from a report that went through", stderr)
	}
}

func TestADeployedPromotionIsSequencedByTheTimeTheRouterRecordedIt(t *testing.T) {
	dependencies, fixture, console := reportingDeploy(t)
	console.Link(t, fixture.Root)

	if _, stderr, err := deployOnce(t, dependencies, fixture, deployOptions{}); err != nil {
		t.Fatalf("runDeploy err = %v; stderr=%s", err, stderr)
	}

	active, _, err := fixture.Provider.Releases(environment.TierProduction, clitest.FixtureSlug).ReadActive(context.Background(), router.DefaultPointer)
	if err != nil {
		t.Fatal(err)
	}
	if got := console.Reports()[0].GetDeployment().GetPromotion().GetSeq(); got != active.Ts {
		t.Errorf("promotion seq = %d, want the router's ts %d", got, active.Ts)
	}
}

func TestTheDeployReportFileHoldsTheDocumentTheConsoleReceived(t *testing.T) {
	dependencies, fixture, console := reportingDeploy(t)
	console.Link(t, fixture.Root)

	if _, stderr, err := deployOnce(t, dependencies, fixture, deployOptions{}); err != nil {
		t.Fatalf("runDeploy err = %v; stderr=%s", err, stderr)
	}

	sent := console.Reports()[0].GetDeployment()
	if got := readDeployReport(t, fixture.Root); got.GetId() != sent.GetId() || got.GetPromotion().GetId() != sent.GetPromotion().GetId() || got.GetApps()[0].GetStoragePrefix() == "" {
		t.Errorf("file holds %v, want the document the console received: %v", got, sent)
	}
}

func TestADeployFromAnUnlinkedTreeMakesNoCallAndPrintsOneHintNamingOcelLink(t *testing.T) {
	dependencies, fixture, console := reportingDeploy(t)

	_, stderr, err := deployOnce(t, dependencies, fixture, deployOptions{})
	if err != nil {
		t.Fatalf("runDeploy err = %v; stderr=%s", err, stderr)
	}

	if len(console.Reports()) != 0 {
		t.Errorf("the console received %d reports from an unlinked tree, want none", len(console.Reports()))
	}
	if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "`ocel link`") {
		t.Errorf("stderr = %q, want one hint line naming `ocel link`", stderr)
	}
	if got := readDeployReport(t, fixture.Root); got.GetOutcome() != consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_SUCCEEDED {
		t.Errorf("the local report holds %v, want the deployment even when nothing is linked", got)
	}
}

func TestADeployWhoseConsoleIsUnreachableStillSucceedsWithOneWarningNamingTheDeployment(t *testing.T) {
	dependencies, fixture, _ := reportingDeploy(t)
	down := httptest.NewServer(http.NotFoundHandler())
	apiURL := down.URL
	down.Close()
	clitest.LinkToConsole(t, fixture.Root, apiURL)
	dependencies.Console = clitest.SignedInTo(apiURL)

	_, stderr, err := deployOnce(t, dependencies, fixture, deployOptions{})
	if err != nil {
		t.Fatalf("runDeploy err = %v, want a deploy to succeed whatever the console does; stderr=%s", err, stderr)
	}

	id := readDeployReport(t, fixture.Root).GetId()
	if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 || !strings.Contains(lines[0], id) {
		t.Errorf("stderr = %q, want one warning line naming deployment %s", stderr, id)
	}
}

func TestAFailedDeployReportsAFailedDeploymentThatMadeNothingLive(t *testing.T) {
	dependencies, fixture, console := reportingDeploy(t)
	console.Link(t, fixture.Root)
	fixture.Provider.FakeStacks().Entering(func(provider.StackSpec) error { return errors.New("simulated deploy failure") })

	_, _, err := deployOnce(t, dependencies, fixture, deployOptions{})
	if err == nil {
		t.Fatal("runDeploy err = nil, want the simulated failure")
	}

	reports := console.Reports()
	if len(reports) != 1 {
		t.Fatalf("the console received %d reports, want the failed deployment", len(reports))
	}
	got := reports[0].GetDeployment()
	if got.GetOutcome() != consolev1.DeploymentOutcome_DEPLOYMENT_OUTCOME_FAILED || got.GetPromotion() != nil || !strings.Contains(got.GetError(), "simulated deploy failure") {
		t.Errorf("deployment = %v, want failed with the error and no promotion", got)
	}
}

func TestADryDeployReportsNothing(t *testing.T) {
	dependencies, fixture, console := reportingDeploy(t)
	console.Link(t, fixture.Root)

	if _, stderr, err := deployOnce(t, dependencies, fixture, deployOptions{dry: true}); err != nil {
		t.Fatalf("runDeploy err = %v; stderr=%s", err, stderr)
	}

	if len(console.Reports()) != 0 {
		t.Errorf("the console received %d reports from a dry run, want none: it made nothing live", len(console.Reports()))
	}
}

func removeWithConsole(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies, opts previewRemoveOptions) (stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)
	if err := runPreviewRemove(context.Background(), dependencies, fixture.Root, opts, &out, &errOut, strings.NewReader("")); err != nil {
		t.Fatalf("runPreviewRemove err = %v; stdout=%s stderr=%s", err, out.String(), errOut.String())
	}
	return errOut.String()
}

func TestRemovingAPreviewFromALinkedTreeRecordsOneEnvironmentEvent(t *testing.T) {
	fixture := setUpPreviewProject(t)
	dependencies := previewDependencies("feature/login", "")
	previewUp(t, fixture, dependencies, previewUpOptions{name: "release-v2"})
	console := clitest.ServeConsole(t)
	console.Link(t, fixture.Root)
	dependencies.Console = clitest.SignedInTo(console.URL)

	stderr := removeWithConsole(t, fixture, dependencies, previewRemoveOptions{name: "release-v2"})

	events := console.Events()
	if len(events) != 1 {
		t.Fatalf("the console received %d environment events, want 1; stderr=%s", len(events), stderr)
	}
	got := events[0].GetEvent()
	if got.GetKind() != consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_PREVIEW_REMOVED || got.GetEnvironment().GetIdentity() != "release-v2" {
		t.Errorf("event = %v, want release-v2 removed", got)
	}
	if events[0].GetProjectId() != clitest.FixtureConsoleProjectID || stderr != "" {
		t.Errorf("project %q stderr %q, want the linked project and no output", events[0].GetProjectId(), stderr)
	}
}

func TestRemovingAPreviewFromAnUnlinkedTreeMakesNoCallAndPrintsOneHintNamingOcelLink(t *testing.T) {
	fixture := setUpPreviewProject(t)
	dependencies := previewDependencies("feature/login", "")
	previewUp(t, fixture, dependencies, previewUpOptions{name: "release-v2"})
	console := clitest.ServeConsole(t)
	dependencies.Console = clitest.SignedInTo(console.URL)

	stderr := removeWithConsole(t, fixture, dependencies, previewRemoveOptions{name: "release-v2"})

	if len(console.Events()) != 0 {
		t.Errorf("the console received %d events from an unlinked tree, want none", len(console.Events()))
	}
	if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "`ocel link`") {
		t.Errorf("stderr = %q, want one hint line naming `ocel link`", stderr)
	}
}
