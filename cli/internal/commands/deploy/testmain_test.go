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
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
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

type envOptions struct {
	folder      string
	environment string
	preview     bool
}

type envRefOptions struct {
	project string
}

func valueTier(opts envOptions) environmentv1.Tier {
	if opts.preview {
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_PRODUCTION
}

func envSet(t *testing.T, _ string, key, value string, opts envOptions) {
	t.Helper()
	seedCell(t, valueTier(opts), &envvarsv1.Coordinate{Slug: "test-app", Folder: opts.folder, Key: key, Environment: opts.environment}, clitest.FakeCellData{Value: value})
}

func envRef(t *testing.T, _ string, key string, opts envOptions, ref envRefOptions) {
	t.Helper()
	seedCell(t, valueTier(opts), &envvarsv1.Coordinate{Slug: "test-app", Key: key}, clitest.FakeCellData{Target: &clitest.FakeCoordinate{Slug: ref.project, Key: key}})
}

func ownedElsewhere(t *testing.T, key, value string) {
	t.Helper()
	seedCell(t, environmentv1.Tier_TIER_PRODUCTION, &envvarsv1.Coordinate{Slug: "platform", Key: key}, clitest.FakeCellData{Value: value})
}

func seedCell(t *testing.T, tier environmentv1.Tier, c *envvarsv1.Coordinate, data clitest.FakeCellData) {
	t.Helper()
	store, err := clitest.LoadFakeStore()
	if err != nil {
		t.Fatalf("load the fake store: %v", err)
	}
	if err := store.Write(tier, c, data); err != nil {
		t.Fatalf("seed %s: %v", c.GetKey(), err)
	}
}
