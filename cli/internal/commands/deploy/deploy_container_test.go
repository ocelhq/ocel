package deploy

import (
	"bytes"
	"context"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func registryProject(t *testing.T, registry string) (Dependencies, string, func() bool) {
	t.Helper()

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	stubAppImages(&dependencies, "api")
	built := false
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, log build.Log) (build.Output, error) {
		built = true
		return buildApps(ctx, cfg, env, archs, log)
	}

	root, _ := clitest.SetUpDeployFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "container,serverless")
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", compute: "container" }],`+registry+`
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), "export {};\n")
	return dependencies, root, func() bool { return built }
}

func TestARegistryWhoseVariableIsUnsetStopsTheDeployBeforeAnythingIsBuilt(t *testing.T) {
	dependencies, root, built := registryProject(t, `
  registry: { server: "ghcr.io", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy() built and deployed a project whose registry password is nowhere to be read, want it refused at the plan")
	}
	said := err.Error() + stdout.String() + stderr.String()
	for _, want := range []string{"OCEL_TEST_REGISTRY_TOKEN", "ghcr.io"} {
		if !strings.Contains(said, want) {
			t.Errorf("runDeploy() failed with %q, want it to mention %q", said, want)
		}
	}
	if built() {
		t.Error("the image was built before the deploy discovered it had nowhere to push it")
	}
}

func TestARegistryPasswordPastedAsATokenIsRefusedWithoutEchoingIt(t *testing.T) {
	const token = "ghp_16C7e42F292c6912E7710c838347Ae178B4a"
	dependencies, root, built := registryProject(t, `
  registry: { server: "ghcr.io", password: "`+token+`" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy() took a pasted token as the name of an environment variable, and would authenticate as nobody")
	}
	said := err.Error() + stdout.String() + stderr.String()
	if strings.Contains(said, token) {
		t.Errorf("the deploy said %q, and the pasted token reached the terminal, the CI log and anything scraping either", said)
	}
	if !strings.Contains(said, "password") {
		t.Errorf("the deploy said %q, want it to name the field that is wrong", said)
	}
	if built() {
		t.Error("the image was built before the deploy discovered its registry credential was nonsense")
	}
}

func TestARegistryWhoseVariableIsSetDeploysAsUsual(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	dependencies, root, _ := registryProject(t, `
  registry: { server: "ghcr.io", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if said := stdout.String() + stderr.String(); strings.Contains(said, "hunter2") {
		t.Errorf("the deploy said %q, and the registry password reached the terminal", said)
	}
}

func TestAProjectThatNamesNoRegistryDemandsNoSecret(t *testing.T) {
	dependencies, root, _ := registryProject(t, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
}

func containerProject(t *testing.T, health string) (Dependencies, string, string) {
	t.Helper()

	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	stubAppImages(&dependencies, "api")

	root, sockPath := clitest.SetUpDeployFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "container,serverless")
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", compute: "container"`+health+` }],
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), "export {};\n")
	return dependencies, root, sockPath
}

func deployContainerProject(t *testing.T, health string) string {
	t.Helper()

	dependencies, root, sockPath := containerProject(t, health)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	clitest.WaitForNoStaleSocket(t, sockPath)
	return stdout.String()
}

func TestAContainerAppReachesTheProviderAsOneDigestPinnedProcess(t *testing.T) {
	out := deployContainerProject(t, "")

	want := "CONTAINER app=api image=" + clitest.FixtureImage("api") + " health=/"
	if !strings.Contains(out, want) {
		t.Errorf("stdout = %q, want %q — the container reaching the provider pinned at the digest the build produced", out, want)
	}
}

func TestAContainerAppContributesZeroFunctions(t *testing.T) {
	out := deployContainerProject(t, "")

	if strings.Contains(out, "FUNCTION ") {
		t.Errorf("stdout = %q, want no function at all for an app one always-on process serves: routing collapses to that process, and a packed zip beside it would be a second answer", out)
	}
}

func TestAContainersHealthPathIsTheOneTheAppNames(t *testing.T) {
	out := deployContainerProject(t, `, health: { path: "/healthz" }`)

	if !strings.Contains(out, "health=/healthz") {
		t.Errorf("stdout = %q, want the health path the app names passed through to the provider", out)
	}
}

func TestAContainerAppRendersADigestPinnedManifestUnderDry(t *testing.T) {
	dependencies, root, sockPath := containerProject(t, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true, dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy --dry err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	out := stdout.String()
	want := clitest.FixtureImage("api") + "  container"
	if !strings.Contains(out, want) {
		t.Errorf("stdout = %q, want the plan to name %q — a dry run renders the manifest a real deploy would send, digest and all", out, want)
	}
	clitest.WaitForNoStaleSocket(t, sockPath)
}

func TestTheRegistryTheProjectNamesRidesTheDeployWithItsSecretResolved(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	dependencies, root, _ := registryProject(t, `
  registry: { server: "ghcr.io", username: "acme-bot", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	want := "REGISTRY server=ghcr.io namespace= username=acme-bot secret=true"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want %q — the push is an engine resource, so the registry it pushes to reaches the release", stdout.String(), want)
	}
}

func TestADeployThatNamesNoRegistrySendsNone(t *testing.T) {
	dependencies, root, _ := registryProject(t, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "REGISTRY ") {
		t.Errorf("stdout = %q, want no registry on a deploy whose project names none: the provider resolves its own inside the deploy, so its credentials never cross the CLI", stdout.String())
	}
}

func TestAServerlessOnlyDeployStillSendsTheRegistryItsFunctionsMayBeRunFrom(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	root, _ := clitest.SetUpDeployFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", compute: "serverless", framework: "node" }],
  registry: { server: "ghcr.io", password: "${OCEL_TEST_REGISTRY_TOKEN}" },
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), "export {};\n")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	want := "REGISTRY server=ghcr.io"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want %q — a provider that runs its functions from images pushes them somewhere too", stdout.String(), want)
	}
}

func TestTheImageIsBuiltForTheArchitectureTheProviderSaysItsContainersRunOn(t *testing.T) {
	dependencies, root, _ := registryProject(t, "")
	t.Setenv(clitest.FakeContainerArchEnvVar, "arm64")
	var required, built map[string]string
	dependencies.RefuseUnbuildableImages = func(_ context.Context, _ *run.Span, _ *project.Project, archs map[string]string) error {
		required = archs
		return nil
	}
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, log build.Log) (build.Output, error) {
		built = archs
		return buildApps(ctx, cfg, env, archs, log)
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	want := map[string]string{"api": "arm64"}
	if !maps.Equal(required, want) {
		t.Errorf("the builder was checked for %v, want %v: a daemon that cannot build for the target is refused before anything is provisioned", required, want)
	}
	if !maps.Equal(built, want) {
		t.Errorf("the image was built for %v, want %v, what the provider names for the app: left to this machine's own architecture, the target may not run it", built, want)
	}
}

func TestAnAppThatFallsBackToContainerIsBuiltForTheArchitectureTheProviderNames(t *testing.T) {
	dependencies, root, _ := registryProject(t, "")
	t.Setenv(clitest.FakeComputesEnvVar, "container")
	t.Setenv(clitest.FakeContainerArchEnvVar, "arm64")
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { fake: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api" }],
};
`)
	var required, built map[string]string
	dependencies.RefuseUnbuildableImages = func(_ context.Context, _ *run.Span, _ *project.Project, archs map[string]string) error {
		required = archs
		return nil
	}
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, log build.Log) (build.Output, error) {
		built = archs
		return buildApps(ctx, cfg, env, archs, log)
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	want := map[string]string{"api": "arm64"}
	if !maps.Equal(required, want) {
		t.Errorf("the builder was checked for %v, want %v: an app the provider runs in a container is one whether or not the config says so", required, want)
	}
	if !maps.Equal(built, want) {
		t.Errorf("the image was built for %v, want %v, what the provider names for an app that falls back to its container compute", built, want)
	}
}

func TestAPrebuiltDeployDoesNotAskWhetherThisMachineCanBuildImages(t *testing.T) {
	dependencies, root, built := registryProject(t, "")
	asked := false
	dependencies.RefuseUnbuildableImages = func(context.Context, *run.Span, *project.Project, map[string]string) error {
		asked = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, root, deployOptions{yes: true, prebuilt: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if asked {
		t.Error("a --prebuilt deploy asked whether this machine can build the image it will not build")
	}
	if built() {
		t.Error("a --prebuilt deploy built the app")
	}
}
