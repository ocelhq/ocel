package root

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/provider"
)

func writeBuildConfig(t *testing.T, root, apps string) {
	t.Helper()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  apps: `+apps+`,
};
`)
}

func TestBuildRefusesAFunctionAppWhoseDirectorySaysNothingAboutWhatItIsBuiltWith(t *testing.T) {
	root, sockPath := clitest.SetUpDeployFixture(t)
	writeBuildConfig(t, root, `[{ name: "web", path: "web" }]`)
	clitest.WriteFile(t, filepath.Join(root, "web", "main.rb"), "puts 1\n")

	deps := clitest.NewDeps()
	clitest.StubBuild(&deps, nil)
	var out strings.Builder
	clitest.AttachTerminalSink(deps, &out)

	err := runBuild(context.Background(), deps, root)
	if err == nil {
		t.Fatal("runBuild = nil error, want the app refused: its provider runs it serverless, and nothing says what web's functions are built with")
	}
	for _, want := range []string{`app "web"`, "framework"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, missing %q", out.String(), want)
		}
	}
	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestBuildAsksTheProviderWhichComputeAnAppNamingNoneRunsOn(t *testing.T) {
	root, sockPath := clitest.SetUpDeployFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "container")
	writeBuildConfig(t, root, `[{ name: "web", path: "web" }]`)
	clitest.WriteFile(t, filepath.Join(root, "web", "package.json"), "{}\n")

	deps := clitest.NewDeps()
	var built *project.Project
	deps.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]map[string]string, _ map[string]string, _ build.Log) (build.Output, error) {
		built = cfg
		return build.Output{}, nil
	}

	if err := runBuild(context.Background(), deps, root); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if built == nil || !built.Apps[0].RunsOn(provider.ComputeContainer) {
		t.Fatalf("built = %+v, want web built for the container compute its provider runs, as a deploy would", built)
	}
	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestBuildRefusesAnAppNamingNoComputeWhenNoProviderCanChooseOne(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: ".", framework: "node" }],
};
`)

	deps := clitest.NewDeps()
	built := false
	deps.BuildApps = func(context.Context, *project.Project, map[string]map[string]string, map[string]string, build.Log) (build.Output, error) {
		built = true
		return build.Output{}, nil
	}

	err := runBuild(context.Background(), deps, root)
	if err == nil {
		t.Fatal("runBuild = nil error, want web refused: nothing says whether it deploys as functions or as an image")
	}
	for _, want := range []string{`"web"`, "`compute`", "provider"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, missing %q", err, want)
		}
	}
	if built {
		t.Error("the build ran before the refusal")
	}
}
