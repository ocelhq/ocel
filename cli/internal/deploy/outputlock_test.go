package deploy

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/workdir"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func holdsBuildOutput(t *testing.T, dependencies *Dependencies) *bool {
	t.Helper()
	held := new(bool)
	built := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error) {
		tried, cancel := context.WithCancel(ctx)
		cancel()
		release, err := workdir.LockBuildOutput(tried, cfg.Dir, func() {})
		if err == nil {
			release()
		}
		*held = err != nil
		return built(ctx, cfg, variables, archs, workers, host, log)
	}
	return held
}

func TestADeployHoldsTheBuildOutputWhileItBuilds(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	held := holdsBuildOutput(t, &dependencies)
	fixture := setUpDeployProject(t)

	deployed(t, dependencies, fixture, deployOptions{yes: true})

	if !*held {
		t.Error("another run could take the build output while this deploy built into it, want it held from the build to the deploy that ships it")
	}
}

func TestADeployNamesItsWaitForAnotherRunsBuildOutputUnderBuild(t *testing.T) {
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	fixture := setUpDeployProject(t)
	release, err := workdir.LockBuildOutput(context.Background(), fixture.Root, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	dependencies.Events.Attach(onStarted(func(title string) {
		if strings.HasPrefix(title, "Waiting for another ocel run") {
			release()
		}
	}))

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := clitest.FinishWithin(t, 10*time.Second, func(ctx context.Context) error {
		return runDeploy(ctx, dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	}); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	assertReportedUnder(t, stdout.String(), "Waited for another ocel run in "+fixture.Root+" to finish with "+buildoutput.Dir, "build")
}
