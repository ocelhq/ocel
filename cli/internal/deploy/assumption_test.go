package deploy

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/environment"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

const newProjectWarning = `creating new project "test-app" without confirmation (nobody to ask)`

func summaryAssumed(t *testing.T, stream string) []*streamv1.Assumption {
	t.Helper()
	evs := envelopes(t, stream)
	return evs[len(evs)-1].GetSummary().GetAssumed()
}

func TestADeployOfANewProjectWithNoTerminalWarnsAndListsTheAssumption(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)
	recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")

	out := deployOutput(t, fixture, dependencies, deployOptions{}, "")
	if !strings.Contains(out, newProjectWarning) {
		t.Errorf("stdout missing %q:\n%s", newProjectWarning, out)
	}
}

func TestTheSummaryOfADeployOfANewProjectWithNoTerminalListsNewProjectAsAssumed(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)
	recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	assumed := summaryAssumed(t, stream.String())
	if len(assumed) != 1 || assumed[0].GetId() != "new_project" || assumed[0].GetWarning() != newProjectWarning {
		t.Fatalf("assumed = %v, want new_project with its warning", assumed)
	}
}

func TestADeployOfANewProjectWithNoTerminalAndNoDomainsWarnsAndListsTheAssumption(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)
	fixture.Provider.Edges().(*fake.Edges).Edge(fake.KindRelay).AddressesItself(true)
	recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
};
`)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	assumed := summaryAssumed(t, stream.String())
	if len(assumed) != 1 || assumed[0].GetId() != "new_project" || assumed[0].GetWarning() != newProjectWarning {
		t.Fatalf("assumed = %v, want new_project with its warning", assumed)
	}
	var warned bool
	for _, ev := range envelopes(t, stream.String()) {
		warned = warned || (ev.GetOperation().GetLevel() == progressv1.Level_LEVEL_WARN && ev.GetOperation().GetMessage() == newProjectWarning)
	}
	if !warned {
		t.Errorf("stream carries no warning event for the skipped guard:\n%s", stream.String())
	}
}

func TestTheSummaryOfADeployAnsweredYesOnATerminalAssumesNothing(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	terminalStdin(&dependencies)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)
	recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
		t.Fatalf("runDeploy err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	if assumed := summaryAssumed(t, stream.String()); len(assumed) != 0 {
		t.Fatalf("assumed = %v, want none for a guard a person answered", assumed)
	}
}

func TestTheSummaryOfADeployGrantedByYesAssumesNothing(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)
	recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	if assumed := summaryAssumed(t, stream.String()); len(assumed) != 0 {
		t.Fatalf("assumed = %v, want --yes to be an answer, not an assumption", assumed)
	}
}

func TestTheSummaryOfADeployOfAKnownProjectAssumesNothing(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)
	recordProjects(t, fixture, environment.TierProduction, "my-application", clitest.FixtureSlug)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	if assumed := summaryAssumed(t, stream.String()); len(assumed) != 0 {
		t.Fatalf("assumed = %v, want no guard raised for a project the backend knows", assumed)
	}
}

func TestTheSummaryOfADeployOnABackendWithNoProjectsAssumesNothing(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	if assumed := summaryAssumed(t, stream.String()); len(assumed) != 0 {
		t.Fatalf("assumed = %v, want the first project on a fresh backend to need no guard", assumed)
	}
}

func TestADryDeployOfANewProjectAssumesNothing(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)
	recordProjects(t, fixture, environment.TierProduction, "my-application", "billing")

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	_ = runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{dry: true}, &stdout, &stderr, strings.NewReader(""))

	evs := envelopes(t, stream.String())
	if len(evs) == 0 {
		t.Fatalf("no events; stderr=%s", stderr.String())
	}
	if assumed := evs[len(evs)-1].GetSummary().GetAssumed(); len(assumed) != 0 {
		t.Fatalf("assumed = %v, want a run that changes nothing to assume nothing", assumed)
	}
}

func TestAPreviewUpOfANewProjectWithNoTerminalWarnsAndListsTheAssumption(t *testing.T) {
	fixture := setUpPreviewProject(t)
	recordProjects(t, fixture, environment.TierPreview, "my-application", "billing")
	dependencies := previewDependencies("feature/login", "")
	useJSONFormat(t, &dependencies)
	opts := previewUpOptions{}
	coverEphemeralPreview(t, fixture, dependencies, opts)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runPreviewUp(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runPreviewUp err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	assumed := summaryAssumed(t, stream.String())
	if len(assumed) != 1 || assumed[0].GetId() != "new_project" || assumed[0].GetWarning() != newProjectWarning {
		t.Fatalf("assumed = %v, want new_project with its warning", assumed)
	}
	var warned bool
	for _, ev := range envelopes(t, stream.String()) {
		warned = warned || (ev.GetOperation().GetLevel() == progressv1.Level_LEVEL_WARN && ev.GetOperation().GetMessage() == newProjectWarning)
	}
	if !warned {
		t.Errorf("stream carries no warning event for the skipped guard:\n%s", stream.String())
	}
}

func TestTheSummaryOfAPreviewUpAnsweredYesOnATerminalAssumesNothing(t *testing.T) {
	fixture := setUpPreviewProject(t)
	recordProjects(t, fixture, environment.TierPreview, "my-application", "billing")
	dependencies := previewDependencies("feature/login", "")
	terminalStdin(&dependencies)
	useJSONFormat(t, &dependencies)
	opts := previewUpOptions{}
	coverEphemeralPreview(t, fixture, dependencies, opts)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runPreviewUp(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
		t.Fatalf("runPreviewUp err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	if assumed := summaryAssumed(t, stream.String()); len(assumed) != 0 {
		t.Fatalf("assumed = %v, want none for a guard a person answered", assumed)
	}
}

func TestTheSummaryOfAPreviewUpDeclinedOnATerminalAssumesNothing(t *testing.T) {
	fixture := setUpPreviewProject(t)
	recordProjects(t, fixture, environment.TierPreview, "my-application", "billing")
	dependencies := previewDependencies("feature/login", "")
	terminalStdin(&dependencies)
	useJSONFormat(t, &dependencies)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runPreviewUp(context.Background(), dependencies, fixture.Root, previewUpOptions{}, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
		t.Fatalf("runPreviewUp err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	if assumed := summaryAssumed(t, stream.String()); len(assumed) != 0 {
		t.Fatalf("assumed = %v, want a declined guard to assume nothing", assumed)
	}
}

func TestTheSummaryOfARemovedPersistentPreviewAnsweredOnATerminalAssumesNothing(t *testing.T) {
	fixture := setUpPreviewProject(t)
	previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{name: "staging", persistent: true})
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	useJSONFormat(t, &dependencies)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	if err := runPreviewRemove(context.Background(), dependencies, fixture.Root, previewRemoveOptions{name: "staging"}, &stdout, &stderr, strings.NewReader("y\n")); err != nil {
		t.Fatalf("runPreviewRemove err = %v; stream=%s stderr=%s", err, stream.String(), stderr.String())
	}

	if assumed := summaryAssumed(t, stream.String()); len(assumed) != 0 {
		t.Fatalf("assumed = %v, want none for a confirmation a person answered", assumed)
	}
}

func TestARefusedPersistentPreviewRemovalWithNoTerminalAssumesNothing(t *testing.T) {
	fixture := setUpPreviewProject(t)
	previewUp(t, fixture, previewDependencies("feature/login", ""), previewUpOptions{name: "staging", persistent: true})
	dependencies := newTestDependencies()
	useJSONFormat(t, &dependencies)

	var stream, stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stream)
	err := runPreviewRemove(context.Background(), dependencies, fixture.Root, previewRemoveOptions{name: "staging"}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runPreviewRemove err = nil, want the refusal naming --yes")
	}
	if assumed := summaryAssumed(t, stream.String()); len(assumed) != 0 {
		t.Fatalf("assumed = %v, want a refusal to leave nothing assumed", assumed)
	}
}
