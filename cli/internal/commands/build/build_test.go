package build

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func TestBuildBakesTheWorkersAnAppHostsIntoItsBuild(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "api", path: ".", framework: "node", compute: "serverless" }],
};
`)
	source := filepath.Join(root, discovery.DefaultRootDirName, "jobs.ts") + ":4"
	dependencies := newTestDependencies()
	dependencies.CollectDeclarations = func(context.Context, *project.Project, *variables.Declarations, io.Writer, io.Writer) ([]declaration.Resource, error) {
		return []declaration.Resource{{Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Name: "greet", Task: &resourcesv1.TaskConfig{}, Source: source}}, nil
	}
	var handed build.HostedWorkers
	dependencies.BuildApps = func(_ context.Context, _ *project.Project, _ map[string]build.AppVariables, _ map[string]string, workers build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		handed = workers
		return build.Output{Functions: []build.Function{{Route: "index", App: "api"}}}, nil
	}
	dependencies.Events = run.NewBus(time.Now)
	clitest.AttachTerminalSink(dependencies.Invocation, &bytes.Buffer{})
	if err := runBuild(context.Background(), dependencies, root); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if len(handed["api"]) != 1 || handed["api"][0] != source {
		t.Errorf("the build was handed workers %v, want api hosting the default worker that serves greet, so `ocel deploy --prebuilt` runs it", handed)
	}
}

func TestBuildNeedsNoLoginAndNoProvider(t *testing.T) {
	t.Parallel()

	t.Run("builds without login or provider", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "api", path: ".", framework: "node", compute: "serverless" }],
};
`)

		var built *project.Project
		dependencies := newTestDependencies()
		dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
			built = cfg
			return build.Output{Functions: []build.Function{{Route: "index", App: "api"}}}, nil
		}

		var stdout bytes.Buffer
		dependencies.Events = run.NewBus(time.Now)
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runBuild(context.Background(), dependencies, root); err != nil {
			t.Fatalf("runBuild: %v", err)
		}

		if built == nil {
			t.Fatal("runBuild did not build the project")
		}
		if want := "✓ Built 1 function into " + statedir.Name + "/output in "; !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want the summary %q", stdout.String(), want)
		}

		record, err := os.ReadFile(filepath.Join(root, statedir.Name, "output", "client-digests.json"))
		if err != nil {
			t.Fatalf("the build recorded nothing about its client values: %v", err)
		}
		if !strings.Contains(string(record), `"api":{}`) {
			t.Errorf("client-digests.json = %s, want it to record the app with nothing inlined: `ocel build` resolves no declared value", record)
		}
	})

	t.Run("builds a container app's image and names what it built", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [
    { name: "api", path: "api", framework: "node", compute: "serverless" },
    { name: "web", path: "web", compute: "container", arch: "arm64" },
  ],
};
`)
		clitest.WriteFile(t, filepath.Join(root, "api", "index.js"), "export {};\n")
		clitest.WriteFile(t, filepath.Join(root, "web", "server.js"), "export {};\n")

		var archs map[string]string
		dependencies := newTestDependencies()
		dependencies.BuildApps = func(_ context.Context, _ *project.Project, _ map[string]build.AppVariables, asked map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
			archs = asked
			return build.Output{
				Functions: []build.Function{{Route: "index", App: "api"}},
				Images:    map[string]string{"web": clitest.FixtureImage("web")},
			}, nil
		}

		var stdout bytes.Buffer
		dependencies.Events = run.NewBus(time.Now)
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runBuild(context.Background(), dependencies, root); err != nil {
			t.Fatalf("runBuild: %v", err)
		}

		if want := map[string]string{"web": "arm64"}; !maps.Equal(archs, want) {
			t.Errorf("the build was asked for images of %v, want %v: a container app is built for the architecture it declares", archs, want)
		}
		for _, want := range []string{"1 function", "web", clitest.FixtureImage("web")} {
			if !strings.Contains(stdout.String(), want) {
				t.Errorf("stdout = %q, want it to name %q", stdout.String(), want)
			}
		}
	})

	t.Run("surfaces a build failure", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)

		dependencies := newTestDependencies()
		dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
			return build.Output{}, errors.New("boom: app build failed")
		}

		var stdout bytes.Buffer
		dependencies.Events = run.NewBus(time.Now)
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runBuild(context.Background(), dependencies, root); err == nil {
			t.Fatal("runBuild err = nil, want the build to fail")
		}
		if !strings.Contains(stdout.String(), "✗ Build failed in ") || !strings.Contains(stdout.String(), "boom: app build failed") {
			t.Fatalf("stdout = %q, want the build failure surfaced in the run's summary", stdout.String())
		}
	})
}

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
	root := clitest.SetUpProject(t).Root
	writeBuildConfig(t, root, `[{ name: "web", path: "web" }]`)
	clitest.WriteFile(t, filepath.Join(root, "web", "main.rb"), "puts 1\n")

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	var out strings.Builder
	clitest.AttachTerminalSink(dependencies.Invocation, &out)

	err := runBuild(context.Background(), dependencies, root)
	if err == nil {
		t.Fatal("runBuild = nil error, want the app refused: its provider runs it serverless, and nothing says what web's functions are built with")
	}
	for _, want := range []string{`app "web"`, "framework"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, missing %q", out.String(), want)
		}
	}
}

func TestBuildAsksTheProviderWhichComputeAnAppNamingNoneRunsOn(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	fixture.Provider.WithFacts(func(facts *provider.Facts) { facts.Computes = []provider.Compute{provider.ComputeContainer} })
	root := fixture.Root
	writeBuildConfig(t, root, `[{ name: "web", path: "web" }]`)
	clitest.WriteFile(t, filepath.Join(root, "web", "package.json"), "{}\n")

	dependencies := newTestDependencies()
	var built *project.Project
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		built = cfg
		return build.Output{}, nil
	}

	if err := runBuild(context.Background(), dependencies, root); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if built == nil || !built.Apps[0].RunsOn(provider.ComputeContainer) {
		t.Fatalf("built = %+v, want web built for the container compute its provider runs, as a deploy would", built)
	}
}

func TestBuildBakesEachAppsPlaintextClientValuesAsADeployDoes(t *testing.T) {
	root := clitest.SetUpProject(t).Root
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  apps: [{ name: "web", path: ".", framework: "next", compute: "serverless", domains: { production: ["shop.acme.com"] } }],
};
`)

	var env map[string]build.AppVariables
	dependencies := newTestDependencies()
	dependencies.BuildApps = func(_ context.Context, _ *project.Project, handed map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		env = handed
		return build.Output{}, nil
	}

	if err := runBuild(context.Background(), dependencies, root); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if got, want := env["web"].Env[processenv.ClientURLEnvVar], "https://shop.acme.com"; got != want {
		t.Errorf("web was built with %s = %q, want %q", processenv.ClientURLEnvVar, got, want)
	}
}

func TestBuildOfANextAppWithDeclaredComputesSucceedsWithoutCredentials(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	fixture.Provider.WithFacts(func(facts *provider.Facts) { facts.MaxFunctionBytes = 200 << 20 })
	fixture.Provider.Credentials().(*fake.Credentials).Ask("sign in to fake", provider.Question{Finding: "no session", Prompt: "Sign in?"})
	root := fixture.Root
	writeBuildConfig(t, root, `[{ name: "web", path: "web", framework: "next", compute: "serverless" }]`)
	clitest.WriteFile(t, filepath.Join(root, "web", "package.json"), "{}\n")

	dependencies := newTestDependencies()
	var host build.Host
	dependencies.BuildApps = func(_ context.Context, _ *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, handed build.Host, _ build.Log) (build.Output, error) {
		host = handed
		return build.Output{}, nil
	}

	if err := runBuild(context.Background(), dependencies, root); err != nil {
		t.Fatalf("runBuild: %v, want the build to read the provider's facts without asking who is signed in", err)
	}
	if host.MaxFunctionBytes != 200<<20 {
		t.Errorf("web was built against a size budget of %d bytes, want the one its provider declares", host.MaxFunctionBytes)
	}
}

func TestBuildRefusesANextFunctionAppWhenNoProviderIsConfiguredToReadItsFactsFrom(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: ".", framework: "next", compute: "serverless" }],
};
`)

	dependencies := newTestDependencies()
	built := false
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		built = true
		return build.Output{}, nil
	}

	err := runBuild(context.Background(), dependencies, root)
	if err == nil || !strings.Contains(err.Error(), "no provider configured") {
		t.Fatalf("runBuild = %v, want web refused: a Next function app is built against its provider's facts, and none is configured", err)
	}
	if built {
		t.Error("the build ran before the refusal")
	}
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

	dependencies := newTestDependencies()
	built := false
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		built = true
		return build.Output{}, nil
	}

	err := runBuild(context.Background(), dependencies, root)
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

func TestBuildingAGoProjectUnpacksNoNodeBundle(t *testing.T) {
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "go.mod"), "module fixture\n\ngo 1.24\n")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{
  "slug": "go-shop",
  "provider": { "fake": {} }
}
`)

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)

	var out bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)
	if err := runBuild(context.Background(), dependencies, root); err != nil {
		t.Fatalf("runBuild err = %v; out=%s", err, out.String())
	}
	if _, err := os.Stat(node.DistDir(root)); err == nil {
		t.Fatalf("%s was unpacked for a project with no JavaScript", node.DistDir(root))
	}
}

func TestBuildUnpacksTheNodeBundleForAJavaScriptAppBesideNoRootPackageJSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "apps/web", compute: "serverless" }],
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "package.json"), `{"name":"web"}`)

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	if err := runBuild(context.Background(), dependencies, root); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if _, err := os.Stat(node.BuildScriptPath(root)); err != nil {
		t.Fatalf("the node build script is not at %s: web is a node app, and its build runs through that script: %v", node.BuildScriptPath(root), err)
	}
}
