package deploy

import (
	"context"
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/projecteditor"
	"github.com/ocelhq/ocel/cli/internal/run"
)

func TestMain(m *testing.M) {
	clitest.AddFakeProviderIDs()
	if os.Getenv(clitest.FakeProviderEnvVar) == "1" {
		os.Exit(clitest.RunFakeProvider())
	}
	clitest.UnsetColorEnv()
	done := clitest.IsolateConfigHome()
	code := m.Run()
	done()
	os.Exit(code)
}

func newTestDependencies() Dependencies {
	return Dependencies{
		Invocation:              clitest.NewInvocation(),
		BuildApps:               build.Apps,
		RefuseUnbuildableImages: build.RefuseUnbuildableImages,
		ReadPrebuilt:            build.ReadPrebuilt,
		DeploymentID:            build.DeploymentID,
		CollectDeclarations:     declaration.Collect,
		ServeVariableEditor:     projecteditor.Serve,
		DiscoverPRNumber:        func() string { return os.Getenv(PRNumberEnvVar) },
	}
}

func stubBuild(dependencies *Dependencies, functions []build.Function) {
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.Log) (build.Output, error) {
		return build.Output{Functions: functions}, nil
	}
	dependencies.ReadPrebuilt = func(context.Context, *project.Project, map[string]string) (build.Output, error) {
		return build.Output{Functions: functions}, nil
	}
	stubRecordedDeploymentIDs(dependencies)
}

func stubAppImages(dependencies *Dependencies, apps ...string) {
	refs := make(map[string]string, len(apps))
	for _, app := range apps {
		refs[app] = clitest.FixtureImage(app)
	}
	dependencies.RefuseUnbuildableImages = func(context.Context, *run.Span, *project.Project, map[string]string) error {
		return nil
	}
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, log build.Log) (build.Output, error) {
		built, err := buildApps(ctx, cfg, env, archs, log)
		built.Images = refs
		return built, err
	}
	readPrebuilt := dependencies.ReadPrebuilt
	dependencies.ReadPrebuilt = func(ctx context.Context, cfg *project.Project, archs map[string]string) (build.Output, error) {
		built, err := readPrebuilt(ctx, cfg, archs)
		built.Images = refs
		return built, err
	}
}

func stubRecordedDeploymentIDs(dependencies *Dependencies) {
	dependencies.DeploymentID = func(_, app string) (string, error) { return clitest.FixtureDeploymentID(app), nil }
}
