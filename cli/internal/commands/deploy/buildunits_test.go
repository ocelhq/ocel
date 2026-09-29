package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func twoAppFixture(t *testing.T) (Dependencies, string) {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONLogFormat(t, &dependencies)
	root, _ := clitest.SetUpDeployFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: { location: "zone-b" } },
  apps: [
    { name: "web", path: "apps/web", framework: "node" },
    { name: "api", path: "apps/api", framework: "node" },
  ],
};
`)
	writeAppSource(t, root, "web", "api")
	return dependencies, root
}

func buildingEach(failing string) func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.Log) (build.Output, error) {
	return func(_ context.Context, cfg *project.Project, _ map[string]map[string]string, _ map[string]string, out build.Log) (build.Output, error) {
		_, _ = io.WriteString(out.Shared, "the builder started\n")
		for _, app := range cfg.Apps {
			log, ended := out.App(app.Name)
			_, _ = fmt.Fprintf(log, "compiling %s\n", app.Name)
			if app.Name == failing {
				err := errors.New(app.Name + " did not compile")
				ended(err)
				return build.Output{}, err
			}
			ended(nil)
		}
		return build.Output{}, nil
	}
}

type buildScope struct {
	subject, message string
	started, ended   int
	status           progressv1.SpanStatus
	output           []string
}

func buildScopes(t *testing.T, stream string) ([]*buildScope, []string) {
	t.Helper()
	var scopes []*buildScope
	bySpan := map[string]*buildScope{}
	var phaseOutput []string
	for i, ev := range envelopes(t, stream) {
		if ev.GetPhase() != progressv1.Phase_PHASE_BUILD {
			continue
		}
		span := string(ev.GetSpanId())
		switch body := ev.GetBody().(type) {
		case *streamv1.RunEvent_Started:
			if ev.GetSubject() == "" || ev.GetLevel() == progressv1.Level_LEVEL_DEBUG {
				continue
			}
			scope := &buildScope{subject: ev.GetSubject(), message: ev.GetMessage(), started: i}
			bySpan[span] = scope
			scopes = append(scopes, scope)
		case *streamv1.RunEvent_Ended:
			if scope, ok := bySpan[span]; ok {
				scope.ended, scope.status = i, body.Ended.GetStatus()
			}
		case *streamv1.RunEvent_Output:
			if scope, ok := bySpan[span]; ok {
				scope.output = append(scope.output, ev.GetMessage())
			} else {
				phaseOutput = append(phaseOutput, ev.GetMessage())
			}
		}
	}
	return scopes, phaseOutput
}

func TestEachAppBuildsAsAUnitOfItsOwnInTheBuildPhaseOnceTheDeclarationsAreCollected(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.BuildApps = buildingEach("")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
	}

	scopes, phaseOutput := buildScopes(t, stdout.String())
	var got []string
	for _, scope := range scopes {
		got = append(got, scope.subject+": "+scope.message)
	}
	want := []string{
		clitest.FixtureSlug + ": Collecting the resources " + clitest.FixtureSlug + " declares",
		"web: Building app web",
		"api: Building app api",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("build units =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for i, scope := range scopes {
		if scope.status != progressv1.SpanStatus_SPAN_STATUS_OK {
			t.Errorf("%s ended %s, want OK", scope.subject, scope.status)
		}
		if i > 0 && scopes[i-1].ended > scope.started {
			t.Errorf("%s started before %s ended, want each unit to end before the next begins", scope.subject, scopes[i-1].subject)
		}
	}
	for _, scope := range scopes[1:] {
		if want := []string{"compiling " + scope.subject}; strings.Join(scope.output, "\n") != want[0] {
			t.Errorf("%s's output = %q, want %q", scope.subject, scope.output, want)
		}
	}
	if strings.Join(phaseOutput, "\n") != "the builder started" {
		t.Errorf("build phase output = %q, want what no app's build said and nothing else", phaseOutput)
	}
}

func TestAnAppWhoseBuildFailsEndsItsOwnUnitInFailureAndTheDeployWithIt(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.BuildApps = buildingEach("api")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runDeploy succeeded, want api's build failure")
	}

	scopes, _ := buildScopes(t, stdout.String())
	statuses := map[string]progressv1.SpanStatus{}
	for _, scope := range scopes {
		statuses[scope.subject] = scope.status
	}
	if statuses["web"] != progressv1.SpanStatus_SPAN_STATUS_OK || statuses["api"] != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Errorf("unit statuses = %v, want web OK and api ERROR", statuses)
	}
	if statuses[clitest.FixtureSlug] != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("the collecting unit ended %s, want OK: it finished before any app built", statuses[clitest.FixtureSlug])
	}
}

func TestEachAppsBuildPrintsAsABlockOfItsOwnWhenThatAppFinishes(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.Presentation = func(io.Writer) terminal.Presentation { return terminal.Resolve(terminal.Conditions{}) }
	dependencies.BuildApps = buildingEach("")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s", err, stdout.String())
	}

	out := stdout.String()
	for _, app := range []string{"web", "api"} {
		block := "INFO  [build] ✓ " + app + ": Built app " + app + " in <1s\n\n    compiling " + app + "\n"
		if !strings.Contains(out, block) {
			t.Errorf("stdout = %q, want %s's block %q", out, app, block)
		}
	}
	if web, api := strings.Index(out, "✓ web: Built"), strings.Index(out, "✓ api: Built"); web < 0 || api < web {
		t.Errorf("stdout = %q, want web's block before api's, in the order they finished", out)
	}
}

func TestABuilderFailureOutsideEveryAppsBuildEndsAUnitOfItsOwnHoldingWhatTheBuilderSaid(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.BuildApps = func(_ context.Context, _ *project.Project, _ map[string]map[string]string, _ map[string]string, out build.Log) (build.Output, error) {
		_, _ = io.WriteString(out.Shared, "Error: Cannot find module 'esbuild'\n")
		return build.Output{}, errors.New("node-builder failed (exit status 1): Error: Cannot find module 'esbuild'")
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runDeploy succeeded, want the builder's failure")
	}

	scopes, phaseOutput := buildScopes(t, stdout.String())
	last := scopes[len(scopes)-1]
	if last.subject != clitest.FixtureSlug || last.message != "Building 2 apps (web and api)" || last.status != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Fatalf("the last build unit = %s: %q ended %s, want %s: \"Building 2 apps (web and api)\" ended in error", last.subject, last.message, last.status, clitest.FixtureSlug)
	}
	if strings.Join(last.output, "\n") != "Error: Cannot find module 'esbuild'" {
		t.Errorf("the failed unit's output = %q, want what the builder said", last.output)
	}
	if len(phaseOutput) != 0 {
		t.Errorf("build phase output = %q, want none: the builder's words belong to the unit that failed", phaseOutput)
	}
}

func TestAnAppsOwnBuildFailureEndsNoSecondUnit(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.BuildApps = buildingEach("web")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runDeploy succeeded, want web's build failure")
	}

	scopes, phaseOutput := buildScopes(t, stdout.String())
	var got []string
	for _, scope := range scopes {
		got = append(got, scope.subject+" "+scope.status.String())
	}
	want := []string{clitest.FixtureSlug + " SPAN_STATUS_OK", "web SPAN_STATUS_ERROR"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("build units = %q, want %q: web's unit already reports the failure", got, want)
	}
	if strings.Join(phaseOutput, "\n") != "the builder started" {
		t.Errorf("build phase output = %q, want what the builder said before web's build", phaseOutput)
	}
}

func TestAFailureAssemblingTheManifestAfterTheBuildsEndsAUnitOfItsOwn(t *testing.T) {
	dependencies, root := twoAppFixture(t)
	dependencies.BuildApps = buildingEach("")
	dependencies.DeploymentID = func(string, string) (string, error) {
		return "", errors.New("no deployment id for app \"web\"; run `ocel build`")
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err == nil {
		t.Fatal("runDeploy succeeded, want the manifest's failure")
	}

	scopes, _ := buildScopes(t, stdout.String())
	last := scopes[len(scopes)-1]
	if last.subject != clitest.FixtureSlug || last.message != "Assembling the deploy manifest of "+clitest.FixtureSlug || last.status != progressv1.SpanStatus_SPAN_STATUS_ERROR {
		t.Errorf("the last build unit = %s: %q ended %s, want the manifest's unit ended in error", last.subject, last.message, last.status)
	}
}
