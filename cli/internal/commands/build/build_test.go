package build

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/processenv"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/variablestore"
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
	clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")
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
	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
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
		clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")

		var built *project.Project
		dependencies := newTestDependencies()
		dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
			built = cfg
			return build.Output{Functions: []build.Function{{Route: "index", App: "api"}}}, nil
		}

		var stdout bytes.Buffer
		dependencies.Events = run.NewBus(time.Now)
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
			t.Fatalf("runBuild: %v", err)
		}

		if built == nil {
			t.Fatal("runBuild did not build the project")
		}
		if want := "✓ Built 1 function into " + buildoutput.StateDir + "/output in "; !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want the summary %q", stdout.String(), want)
		}

		record, err := os.ReadFile(filepath.Join(root, buildoutput.StateDir, "output", "client-digests.json"))
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
		if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
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
export default {
  slug: "test-app",
  apps: [{ name: "api", path: ".", framework: "node", compute: "serverless" }],
};
`)
		clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")

		dependencies := newTestDependencies()
		dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
			return build.Output{}, errors.New("boom: app build failed")
		}

		var stdout bytes.Buffer
		dependencies.Events = run.NewBus(time.Now)
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runBuild(context.Background(), dependencies, root, options{}, nil); err == nil {
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

	err := runBuild(context.Background(), dependencies, root, options{}, nil)
	if err == nil {
		t.Fatal("runBuild = nil error, want the app refused: its provider runs it serverless, and nothing says what web's functions are built with")
	}
	for _, want := range []string{`app "web"`, "framework"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output = %q, missing %q", out.String(), want)
		}
	}
}

func TestBuildSummarisesEachAppItBuiltWithItsBuildID(t *testing.T) {
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "apps": [{"name": "api", "path": ".", "framework": "node", "compute": "serverless"}]}`)
	clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")

	dependencies := newTestDependencies()
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		return build.Output{Functions: []build.Function{{Route: "index", App: "api"}}, BuildIDs: map[string]string{"api": "0123456789abcdef0123456789abcdef"}}, nil
	}
	dependencies.Presentation = clitest.ResolveJSONPresentation
	var out bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)

	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	events := clitest.RunEvents(t, out.String())
	apps := events[len(events)-1].GetSummary().GetApps()
	if len(apps) != 1 || apps[0].GetApp() != "api" || apps[0].GetOutcome() != progressv1.AppOutcome_APP_OUTCOME_SUCCEEDED || apps[0].GetBuildId() != "0123456789abcdef0123456789abcdef" {
		t.Errorf("summary apps = %v, want api succeeded with the build id it was built under", apps)
	}
}

func TestBuildOfAProjectWithNoAppFailsSayingWhyNoneIsDetectedAtTheRoot(t *testing.T) {
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "provider": {"fake": {}}}`)
	clitest.WriteFile(t, filepath.Join(root, "Dockerfile"), "FROM node:22\n")
	clitest.WriteFile(t, filepath.Join(root, "server.js"), "\n")

	dependencies := newTestDependencies()
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		t.Fatal("built a project with no app")
		return build.Output{}, nil
	}
	clitest.AttachTerminalSink(dependencies.Invocation, &bytes.Buffer{})

	err := runBuild(context.Background(), dependencies, root, options{}, nil)
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeProjectInvalidConfig {
		t.Fatalf("runBuild = %v, want %s", err, clierror.CodeProjectInvalidConfig)
	}
	for _, want := range []string{"Nothing to build", "shop declares no apps", "the project root holds none of package.json", `"apps"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err, want)
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

	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
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

	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if got, want := env["web"].Env[processenv.NextPublicURLEnvVar], "https://shop.acme.com"; got != want {
		t.Errorf("web was built with %s = %q, want %q", processenv.NextPublicURLEnvVar, got, want)
	}
}

func TestBuildHandsEachNextAppTheKeysOfThePublicVariablesDeclaredForIt(t *testing.T) {
	root := clitest.SetUpProject(t).Root
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  apps: [
    { name: "web", path: ".", framework: "next", compute: "serverless" },
    { name: "admin", path: "admin", folder: "/admin", framework: "next", compute: "serverless" },
  ],
};
`)
	clitest.WriteFile(t, filepath.Join(root, "admin", "package.json"), "{}\n")

	dependencies := newTestDependencies()
	dependencies.CollectDeclarations = func(ctx context.Context, _ *project.Project, declarations *variables.Declarations, _, _ io.Writer) ([]declaration.Resource, error) {
		_, err := declarations.DeclareEnv(ctx, &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{
			{Key: "NEXT_PUBLIC_API_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true},
			{Key: "NEXT_PUBLIC_ADMIN_URL", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Folders: []string{"/admin"}},
		}})
		return nil, err
	}
	var env map[string]build.AppVariables
	dependencies.BuildApps = func(_ context.Context, _ *project.Project, handed map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		env = handed
		return build.Output{}, nil
	}

	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if want := []string{"NEXT_PUBLIC_API_URL", processenv.NextPublicURLEnvVar}; !slices.Equal(env["web"].PublicKeys, want) {
		t.Errorf("web public keys = %v, want %v: the adapter inlines every NEXT_PUBLIC_ key declared for the app, though ocel build resolves no value", env["web"].PublicKeys, want)
	}
	if want := []string{"NEXT_PUBLIC_ADMIN_URL", "NEXT_PUBLIC_API_URL", processenv.NextPublicURLEnvVar}; !slices.Equal(env["admin"].PublicKeys, want) {
		t.Errorf("admin public keys = %v, want %v", env["admin"].PublicKeys, want)
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

	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
		t.Fatalf("runBuild: %v, want the build to read the provider's facts without asking who is signed in", err)
	}
	if host.MaxFunctionBytes != 200<<20 {
		t.Errorf("web was built against a size budget of %d bytes, want the one its provider declares", host.MaxFunctionBytes)
	}
}

func TestBuildRefusesANextFunctionAppWhenNoProviderIsConfiguredToReadItsFactsFrom(t *testing.T) {
	t.Parallel()

	for _, compute := range []string{`"serverless"`, `{ container: {} }`} {
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: ".", framework: "next", compute: `+compute+` }],
};
`)
		clitest.WriteFile(t, filepath.Join(root, "package.json"), "{}\n")

		dependencies := newTestDependencies()
		built := false
		dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
			built = true
			return build.Output{}, nil
		}

		err := runBuild(context.Background(), dependencies, root, options{}, nil)
		if err == nil || !strings.Contains(err.Error(), "no provider configured") {
			t.Fatalf("runBuild on compute %s = %v, want web refused: a Next app built by ocel's adapter is built against its provider's facts, and none is configured", compute, err)
		}
		if built {
			t.Errorf("the build on compute %s ran before the refusal", compute)
		}
	}
}

func TestBuildOfANextContainerNoDockerfileBuildsIsBuiltByTheAdapterAgainstItsProvidersFacts(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	fixture.Provider.WithFacts(func(facts *provider.Facts) { facts.MaxFunctionBytes = 200 << 20 })
	root := fixture.Root
	writeBuildConfig(t, root, `[{ name: "web", path: "web", framework: "next", compute: { container: {} } }]`)
	clitest.WriteFile(t, filepath.Join(root, "web", "package.json"), "{}\n")

	dependencies := newTestDependencies()
	var built *project.Project
	var archs map[string]string
	var host build.Host
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, handed map[string]string, _ build.HostedWorkers, facts build.Host, _ build.Log) (build.Output, error) {
		built, archs, host = cfg, handed, facts
		return build.Output{}, nil
	}

	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if built == nil || !built.Apps[0].IsBuiltFromFunction() {
		t.Fatalf("built = %+v, want web built from its function", built)
	}
	if !host.EdgeRunsCode || !host.EdgeDispatchesContainers || host.MaxFunctionBytes != 200<<20 {
		t.Errorf("host = %+v, want the provider's facts, read as a serverless Next app's are, so ocel build and deploy --prebuilt build web alike", host)
	}
	if arch, named := archs["web"]; !named || arch != "" {
		t.Errorf("archs = %v, want web built for the architecture an image of it would be: none named, so the building machine's", archs)
	}
}

func TestBuildOfANextAppTheProviderPutsOnContainersIsBuiltByTheAdapter(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	fixture.Provider.WithFacts(func(facts *provider.Facts) { facts.Computes = []provider.Compute{provider.ComputeContainer} })
	root := fixture.Root
	writeBuildConfig(t, root, `[{ name: "web", path: "web", framework: "next" }]`)
	clitest.WriteFile(t, filepath.Join(root, "web", "package.json"), "{}\n")

	dependencies := newTestDependencies()
	var built *project.Project
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		built = cfg
		return build.Output{}, nil
	}

	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if built == nil || !built.Apps[0].IsBuiltFromFunction() {
		t.Fatalf("built = %+v, want web built from its function on the container compute its provider runs", built)
	}
}

func TestBuildOfANextContainerOnAProviderThatRunsNoFunctionsAndWhoseEdgeDispatchesNoContainerIsBuiltAsAnImage(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	fixture.Provider.WithFacts(func(facts *provider.Facts) { facts.Computes = []provider.Compute{provider.ComputeContainer} })
	fixture.Provider.Edges().(*fake.Edges).Edge(fake.KindRelay).RouterDispatches(false)
	root := fixture.Root
	writeBuildConfig(t, root, `[{ name: "web", path: "web", framework: "next", compute: { container: {} } }]`)
	clitest.WriteFile(t, filepath.Join(root, "web", "package.json"), "{}\n")

	dependencies := newTestDependencies()
	var built *project.Project
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		built = cfg
		return build.Output{}, nil
	}

	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if built == nil || built.Apps[0].IsBuiltFromFunction() || !built.Apps[0].IsBuiltAsImage() {
		t.Fatalf("built = %+v, want web built as an image that runs next start: nothing in front of it dispatches it, and no cache it would share exists", built)
	}
}

func TestBuildRefusesAnAppNamingNoComputeWhenNoProviderCanChooseOne(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "." }],
};
`)

	dependencies := newTestDependencies()
	built := false
	dependencies.BuildApps = func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
		built = true
		return build.Output{}, nil
	}

	err := runBuild(context.Background(), dependencies, root, options{}, nil)
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

func TestBuildingAJavaScriptAppLeavesNoNodeBundleInTheProject(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  apps: [{ name: "web", path: "apps/web", compute: "serverless" }],
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "package.json"), `{"name":"web"}`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "web", "src", "server.ts"), "export {};\n")

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	if err := runBuild(context.Background(), dependencies, root, options{}, nil); err != nil {
		t.Fatalf("runBuild: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, buildoutput.StateDir, "dist")); err == nil {
		t.Fatalf("the build unpacked the node bundle into %s, want it in the user cache where every project and run shares one copy", root)
	}
}

func TestBuildWithAnEnvHandsEachAppTheValuesOfThatTierAsADeployDoes(t *testing.T) {
	fixture := clitest.SetUpProject(t)
	root := fixture.Root
	writeBuildConfig(t, root, `[{ name: "web", path: "web", framework: "next", compute: "serverless" }]`)
	clitest.WriteFile(t, filepath.Join(root, "web", "package.json"), "{}\n")
	store := variablestore.Store{KeyValues: fixture.Provider.KeyValues(), Cipher: fixture.Provider.Cipher()}
	production := variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction}
	for key, value := range map[string]string{"PAGE_ID": "page-123", "STRIPE_API_KEY": "sk_live_123"} {
		if _, err := store.Set(context.Background(), production, variablestore.Coordinate{Cell: variablestore.Cell{Key: key}}, value, nil); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}

	buildWith := func(t *testing.T, opts options) build.AppVariables {
		t.Helper()
		dependencies := newTestDependencies()
		dependencies.CollectDeclarations = func(ctx context.Context, _ *project.Project, declarations *variables.Declarations, _, _ io.Writer) ([]declaration.Resource, error) {
			if err := declarations.Prefetch(ctx); err != nil {
				return nil, err
			}
			_, err := declarations.DeclareEnv(ctx, &resourcesv1.DeclareEnvRequest{Definitions: []*resourcesv1.VariableDefinition{
				{Key: "PAGE_ID", Class: resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, Required: true},
				{Key: "STRIPE_API_KEY", Class: resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, Required: true},
			}})
			return nil, err
		}
		var handed map[string]build.AppVariables
		dependencies.BuildApps = func(_ context.Context, _ *project.Project, values map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
			handed = values
			return build.Output{}, nil
		}
		var out strings.Builder
		clitest.AttachTerminalSink(dependencies.Invocation, &out)
		if err := runBuild(context.Background(), dependencies, root, opts, nil); err != nil {
			t.Fatalf("runBuild: %v\n%s", err, out.String())
		}
		return handed["web"]
	}

	t.Run("--env production puts the plaintext in the build's env and the secret in its live dir", func(t *testing.T) {
		web := buildWith(t, options{env: "production"})
		if got := web.Env["PAGE_ID"]; got != "page-123" {
			t.Errorf("web was built with PAGE_ID = %q, want production's %q", got, "page-123")
		}
		if got := web.Live["STRIPE_API_KEY"]; got != "sk_live_123" {
			t.Errorf("web's live dir holds STRIPE_API_KEY = %q, want production's secret", got)
		}
	})

	t.Run("--env production reads the tier's store and changes nothing in it", func(t *testing.T) {
		buildWith(t, options{env: "production"})
		reads := []string{
			variablestorev1connect.VariableStoreServiceListValuesProcedure,
			variablestorev1connect.VariableStoreServiceRevealValuesProcedure,
			variablestorev1connect.VariableStoreServiceDescribeEnvSourceProcedure,
		}
		for _, procedure := range fixture.Requests.Procedures() {
			if strings.HasPrefix(procedure, "/"+variablestorev1connect.VariableStoreServiceName+"/") && !slices.Contains(reads, procedure) {
				t.Errorf("the build called %s, want only reads of the tier's store: a build changes nothing a deploy reads", procedure)
			}
		}
	})

	t.Run("without --env the build sees no declared value", func(t *testing.T) {
		web := buildWith(t, options{})
		if got, ok := web.Env["PAGE_ID"]; ok {
			t.Errorf("web was built with PAGE_ID = %q, want it unset: no tier was named", got)
		}
		if len(web.Live) != 0 {
			t.Errorf("web's live dir holds %v, want nothing: no tier was named", slices.Collect(maps.Keys(web.Live)))
		}
	})
}

func TestBuildRefusesAnEnvThatNamesNoTier(t *testing.T) {
	root := clitest.SetUpProject(t).Root
	writeBuildConfig(t, root, `[{ name: "api", path: ".", framework: "node", compute: "serverless" }]`)
	clitest.WriteFile(t, filepath.Join(root, "index.ts"), "export {}\n")

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	err := runBuild(context.Background(), dependencies, root, options{env: "staging"}, nil)
	if err == nil || !strings.Contains(err.Error(), `--env is "staging"`) {
		t.Fatalf("runBuild = %v, want --env staging refused naming the tiers it takes", err)
	}
}
