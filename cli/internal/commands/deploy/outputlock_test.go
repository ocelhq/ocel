package deploy

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/workdir"
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
