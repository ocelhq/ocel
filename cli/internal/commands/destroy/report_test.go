package destroy

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func destroyProductionWith(t *testing.T, project clitest.FakeProject, console *clitest.FakeConsole, yes, dry bool) (stderr string) {
	t.Helper()
	invocation := clitest.NewInvocation()
	invocation.DeploymentReports = clitest.SignedInTo(console.URL)
	var out, errOut bytes.Buffer
	clitest.AttachTerminalSink(invocation, &out)
	if err := runDestroyProduction(context.Background(), invocation, project.Root, yes, dry, &out, &errOut, strings.NewReader("")); err != nil {
		t.Fatalf("runDestroyProduction err = %v; stdout=%s stderr=%s", err, out.String(), errOut.String())
	}
	return errOut.String()
}

func TestDestroyingProductionFromALinkedTreeRecordsOneDestroyedEvent(t *testing.T) {
	project := deployedToProduction(t)
	console := clitest.ServeConsole(t)
	console.Link(t, project.Root)

	stderr := destroyProductionWith(t, project, console, true, false)

	events := console.Events()
	if len(events) != 1 {
		t.Fatalf("the console received %d environment events, want 1; stderr=%s", len(events), stderr)
	}
	got := events[0].GetEvent()
	if got.GetKind() != consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_DESTROYED || got.GetEnvironment().GetTier() != environmentv1.Tier_TIER_PRODUCTION {
		t.Errorf("event = %v, want production destroyed", got)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing from an event that went through", stderr)
	}
}

func TestDestroyingThePreviewFootprintRecordsADestroyedEventInThePreviewTier(t *testing.T) {
	project := deployedToPreview(t)
	console := clitest.ServeConsole(t)
	console.Link(t, project.Root)
	invocation := clitest.NewInvocation()
	invocation.DeploymentReports = clitest.SignedInTo(console.URL)
	var out, errOut bytes.Buffer
	clitest.AttachTerminalSink(invocation, &out)

	if err := runDestroyPreviewProject(context.Background(), invocation, project.Root, true, false, &out, &errOut, strings.NewReader("")); err != nil {
		t.Fatalf("runDestroyPreviewProject err = %v; stdout=%s stderr=%s", err, out.String(), errOut.String())
	}

	events := console.Events()
	if len(events) != 1 || events[0].GetEvent().GetKind() != consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_DESTROYED || events[0].GetEvent().GetEnvironment().GetTier() != environmentv1.Tier_TIER_PREVIEW {
		t.Errorf("events = %v, want one destroyed event in the preview tier; stderr=%s", events, errOut.String())
	}
}

func TestDestroyingFromAnUnlinkedTreeMakesNoCallAndPrintsOneHintNamingOcelLink(t *testing.T) {
	project := deployedToProduction(t)
	console := clitest.ServeConsole(t)

	stderr := destroyProductionWith(t, project, console, true, false)

	if len(console.Events()) != 0 {
		t.Errorf("the console received %d events from an unlinked tree, want none", len(console.Events()))
	}
	if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "`ocel link`") {
		t.Errorf("stderr = %q, want one hint line naming `ocel link`", stderr)
	}
}

func TestADryDestroyRecordsNothing(t *testing.T) {
	project := deployedToProduction(t)
	console := clitest.ServeConsole(t)
	console.Link(t, project.Root)

	destroyProductionWith(t, project, console, false, true)

	if len(console.Events()) != 0 {
		t.Errorf("the console received %d events from a dry run, want none: nothing was destroyed", len(console.Events()))
	}
}
