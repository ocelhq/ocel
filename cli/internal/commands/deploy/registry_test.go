package deploy

import (
	"bytes"
	"context"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func registryProject(t *testing.T, registry string) (Dependencies, clitest.FakeProject, func() bool) {
	t.Helper()

	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	stubAppImages(&dependencies, "api")
	built := false
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, workers build.HostedWorkers, log build.Log) (build.Output, error) {
		built = true
		return buildApps(ctx, cfg, env, archs, workers, log)
	}

	fixture := setUpDeployProject(t)
	clitest.ServeImageDaemon(t, "amd64")
	writeConfig(t, fixture.Root, `  apps: [{ name: "api", path: "apps/api", compute: "container" }],`+registry+"\n")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "src", "server.ts"), "export {};\n")
	return dependencies, fixture, func() bool { return built }
}

func TestARegistryWhoseVariableIsUnsetStopsTheDeployBeforeAnythingIsBuilt(t *testing.T) {
	dependencies, fixture, built := registryProject(t, `
  registry: { server: "registry.example.com", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy() built and deployed a project whose registry password is nowhere to be read, want it refused at the plan")
	}
	said := err.Error() + stdout.String() + stderr.String()
	for _, want := range []string{"OCEL_TEST_REGISTRY_TOKEN", "registry.example.com"} {
		if !strings.Contains(said, want) {
			t.Errorf("runDeploy() failed with %q, want it to mention %q", said, want)
		}
	}
	if built() {
		t.Error("the image was built before the deploy discovered it had nowhere to push it")
	}
	if strings.Contains(stdout.String(), "Loaded the provider") {
		t.Errorf("the deploy loaded the provider before reading the password it needs, want it refused first: %s", stdout.String())
	}
}

func TestARegistryThatRefusesThePushStopsTheDeployAtTheCheckBeforeAnythingIsBuilt(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	dependencies, fixture, built := registryProject(t, `
  registry: { server: "registry.example.com/acme", username: "acme-bot", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)
	fixture.Provider.ImageStore().DenyPushes(refusal.Refuse(refusal.CodeDenied, "registry.example.com refuses acme-bot a push to registry.example.com/acme/api: DENIED: permission_denied: create_package"))

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatal("runDeploy() deployed with a registry that refuses the push, want it refused at the check")
	}
	said := err.Error() + stdout.String()
	for _, want := range []string{"registry.example.com", "create_package", "Checking push access to registry.example.com/acme failed"} {
		if !strings.Contains(said, want) {
			t.Errorf("runDeploy() said %q, want it to mention %q", said, want)
		}
	}
	if built() {
		t.Error("the image was built before the deploy found the registry refuses the push")
	}
	if probed := fixture.Provider.ImageStore().Probed(); !slices.Equal(probed, []string{"api"}) {
		t.Errorf("push access probed for %q, want the one container app", probed)
	}
	if strings.Contains(said, "hunter2") {
		t.Errorf("the deploy said %q, and the registry password reached the terminal", said)
	}
}

func TestARegistryPasswordPastedAsATokenIsRefusedWithoutEchoingIt(t *testing.T) {
	const token = "tok_16C7e42F292c6912E7710c838347Ae178B4a"
	dependencies, fixture, built := registryProject(t, `
  registry: { server: "registry.example.com", password: "`+token+`" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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
	dependencies, fixture, _ := registryProject(t, `
  registry: { server: "registry.example.com", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if said := stdout.String() + stderr.String(); strings.Contains(said, "hunter2") {
		t.Errorf("the deploy said %q, and the registry password reached the terminal", said)
	}
}

func TestAProjectThatNamesNoRegistryDemandsNoSecret(t *testing.T) {
	dependencies, fixture, _ := registryProject(t, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
}

func containerProject(t *testing.T, health string) (Dependencies, clitest.FakeProject) {
	t.Helper()

	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	stubAppImages(&dependencies, "api")

	fixture := setUpDeployProject(t)
	clitest.ServeImageDaemon(t, "amd64")
	writeConfig(t, fixture.Root, `  apps: [{ name: "api", path: "apps/api", compute: "container"`+health+` }],`+"\n")
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "src", "server.ts"), "export {};\n")
	return dependencies, fixture
}

func deployContainerProject(t *testing.T, health string) *contractv1.ManifestApp {
	t.Helper()

	dependencies, fixture := containerProject(t, health)
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	return manifestApp(t, sentDeploy(t, fixture).GetManifest(), "api")
}

func TestAContainerAppReachesTheProviderAsOneDigestPinnedProcess(t *testing.T) {
	container := deployContainerProject(t, "").GetContainer()

	if container.GetImage() != clitest.FixtureImage("api") || container.GetHealthCheckPath() != "" {
		t.Errorf("container = %s checked at %q, want %s with no health path — the container reaching the provider pinned at the digest the build produced, and the provider choosing the path for an app that names none", container.GetImage(), container.GetHealthCheckPath(), clitest.FixtureImage("api"))
	}
}

func TestAContainerAppContributesZeroFunctions(t *testing.T) {
	api := deployContainerProject(t, "")

	if functions := api.GetServerless().GetFunctions(); len(functions) != 0 {
		t.Errorf("api carries %d functions, want none at all for an app one always-on process serves: routing collapses to that process, and a packed zip beside it would be a second answer", len(functions))
	}
}

func TestAContainersHealthPathIsTheOneTheAppNames(t *testing.T) {
	container := deployContainerProject(t, `, health: { path: "/healthz" }`).GetContainer()

	if container.GetHealthCheckPath() != "/healthz" {
		t.Errorf("health path = %q, want the health path the app names passed through to the provider", container.GetHealthCheckPath())
	}
}

func TestAContainerAppRendersADigestPinnedManifestUnderDry(t *testing.T) {
	dependencies, fixture := containerProject(t, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true, dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy --dry err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	req := sentDeploy(t, fixture)
	if image := manifestApp(t, req.GetManifest(), "api").GetContainer().GetImage(); !req.GetDry() || image != clitest.FixtureImage("api") {
		t.Errorf("the dry deploy sent dry=%v with image %q, want %q — a dry run plans the manifest a real deploy would send, digest and all", req.GetDry(), image, clitest.FixtureImage("api"))
	}
	if !strings.Contains(stdout.String(), "api  image") {
		t.Errorf("stdout = %q, want the plan to show the image it would push", stdout.String())
	}
}

func TestTheRegistryTheProjectNamesRidesTheDeployWithItsSecretResolved(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	dependencies, fixture, _ := registryProject(t, `
  registry: { server: "registry.example.com", username: "acme-bot", password: "${OCEL_TEST_REGISTRY_TOKEN}" },`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	registry := sentDeploy(t, fixture).GetProjectRegistry()
	if registry.GetServer() != "registry.example.com" || registry.GetNamespace() != "" || registry.GetUsername() != "acme-bot" || registry.GetPassword() != "hunter2" {
		t.Errorf("registry = %s as %q in %q, want registry.example.com as acme-bot with its secret resolved — the push is an engine resource, so the registry it pushes to reaches the release", registry.GetServer(), registry.GetUsername(), registry.GetNamespace())
	}
}

func TestADeployThatNamesNoRegistrySendsNone(t *testing.T) {
	dependencies, fixture, _ := registryProject(t, "")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if registry := sentDeploy(t, fixture).GetProjectRegistry(); registry != nil {
		t.Errorf("registry = %s, want none on a deploy whose project names none: the provider resolves its own inside the deploy, so its credentials never cross the CLI", registry.GetServer())
	}
}

func TestAServerlessOnlyDeployStillSendsTheRegistryItsFunctionsMayBeRunFrom(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	dependencies := newTestDependencies()
	stubBuild(&dependencies, []build.Function{
		{Route: "index", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	fixture := setUpDeployProject(t)
	writeConfig(t, fixture.Root, `  apps: [{ name: "api", path: "apps/api", compute: "serverless", framework: "node" }],
  registry: { server: "registry.example.com", password: "${OCEL_TEST_REGISTRY_TOKEN}" },
`)
	clitest.WriteFile(t, filepath.Join(fixture.Root, "apps", "api", "src", "server.ts"), "export {};\n")

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if server := sentDeploy(t, fixture).GetProjectRegistry().GetServer(); server != "registry.example.com" {
		t.Errorf("registry = %q, want registry.example.com — a provider that runs its functions from images pushes them somewhere too", server)
	}
}

func TestTheImageIsBuiltForTheArchitectureTheProviderSaysItsContainersRunOn(t *testing.T) {
	dependencies, fixture, _ := registryProject(t, "")
	fixture.Provider.WrappingContainers("arm64", []byte(fake.RuntimeBinary))
	clitest.ServeImageDaemon(t, "arm64")
	var required, built map[string]string
	dependencies.RefuseUnbuildableImages = func(_ context.Context, _ *run.Span, _ *project.Project, archs map[string]string) error {
		required = archs
		return nil
	}
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, workers build.HostedWorkers, log build.Log) (build.Output, error) {
		built = archs
		return buildApps(ctx, cfg, env, archs, workers, log)
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
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
	dependencies, fixture, _ := registryProject(t, "")
	fixture.Provider.WithFacts(func(facts *provider.Facts) { facts.Computes = []provider.Compute{provider.ComputeContainer} })
	fixture.Provider.WrappingContainers("arm64", []byte(fake.RuntimeBinary))
	clitest.ServeImageDaemon(t, "arm64")
	writeAppsConfig(t, fixture.Root, `{ name: "api", path: "apps/api" }`)
	var required, built map[string]string
	dependencies.RefuseUnbuildableImages = func(_ context.Context, _ *run.Span, _ *project.Project, archs map[string]string) error {
		required = archs
		return nil
	}
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, env map[string]map[string]string, archs map[string]string, workers build.HostedWorkers, log build.Log) (build.Output, error) {
		built = archs
		return buildApps(ctx, cfg, env, archs, workers, log)
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
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
	dependencies, fixture, built := registryProject(t, "")
	asked := false
	dependencies.RefuseUnbuildableImages = func(context.Context, *run.Span, *project.Project, map[string]string) error {
		asked = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true, prebuilt: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if asked {
		t.Error("a --prebuilt deploy asked whether this machine can build the image it will not build")
	}
	if built() {
		t.Error("a --prebuilt deploy built the app")
	}
}
