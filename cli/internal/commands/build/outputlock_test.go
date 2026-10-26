package build

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/workdir"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func TestBuildHoldsTheBuildOutputWhileItBuilds(t *testing.T) {
	t.Parallel()

	root := writeServerlessProject(t)
	dependencies := newTestDependencies()
	var held bool
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		tried, cancel := context.WithCancel(ctx)
		cancel()
		release, err := workdir.LockBuildOutput(tried, cfg.Dir, func() {})
		if err == nil {
			release()
		}
		held = err != nil
		return build.Output{}, nil
	}
	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if !held {
		t.Error("another run could take the build output while ocel build wrote it, want it held")
	}
}

func TestBuildNamesItsWaitForAnotherRunsBuildOutputUnderBuild(t *testing.T) {
	t.Parallel()

	root := writeServerlessProject(t)
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	release, err := workdir.LockBuildOutput(context.Background(), root, func() {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	dependencies.Events = run.NewBus(time.Now)
	dependencies.Events.Attach(onStarted(func(title string) {
		if strings.HasPrefix(title, "Waiting for another ocel run") {
			release()
		}
	}))
	var stdout bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)

	if err := clitest.FinishWithin(t, 10*time.Second, func(ctx context.Context) error {
		return runBuild(ctx, dependencies, root, options{}, nil)
	}); err != nil {
		t.Fatalf("runBuild: %v; stdout=%s", err, stdout.String())
	}
	assertReportedUnder(t, stdout.String(), "Waited for another ocel run in "+root+" to finish with "+buildoutput.Dir, "build")
}
