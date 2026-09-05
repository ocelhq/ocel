package deploy

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func registryProject(t *testing.T, registry string) (cmddeps.Deps, string, func() bool) {
	t.Helper()

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, nil)
	clitest.StubAppImages(&deps, "api")
	built := false
	deps.BuildAppImages = func(context.Context, *projectconfig.Config, io.Writer) (map[string]string, error) {
		built = true
		return map[string]string{"api": clitest.FixtureImage("api")}, nil
	}

	root, _ := clitest.SetUpDeployFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "container,serverless")
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", compute: "container" }],`+registry+`
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), "export {};\n")
	return deps, root, func() bool { return built }
}

func TestARegistryWhoseVariableIsUnsetStopsTheDeployBeforeAnythingIsBuilt(t *testing.T) {
	deps, root, built := registryProject(t, `
  registry: { server: "ghcr.io", password: "OCEL_TEST_REGISTRY_TOKEN" },`)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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
	deps, root, built := registryProject(t, `
  registry: { server: "ghcr.io", password: "`+token+`" },`)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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
	deps, root, _ := registryProject(t, `
  registry: { server: "ghcr.io", password: "OCEL_TEST_REGISTRY_TOKEN" },`)

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if said := stdout.String() + stderr.String(); strings.Contains(said, "hunter2") {
		t.Errorf("the deploy said %q, and the registry password reached the terminal", said)
	}
}

func TestAProjectThatNamesNoRegistryDemandsNoSecret(t *testing.T) {
	deps, root, _ := registryProject(t, "")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
}

func containerProject(t *testing.T, health string) (cmddeps.Deps, string, string) {
	t.Helper()

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "index", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	clitest.StubAppImages(&deps, "api")

	root, sockPath := clitest.SetUpDeployFixture(t)
	t.Setenv(clitest.FakeComputesEnvVar, "container,serverless")
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", compute: "container"`+health+` }],
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), "export {};\n")
	return deps, root, sockPath
}

func deployContainerProject(t *testing.T, health string) string {
	t.Helper()

	deps, root, sockPath := containerProject(t, health)
	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
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
		t.Errorf("stdout = %q, want the health path the app names carried through to the provider", out)
	}
}

func TestAContainerAppRendersADigestPinnedManifestUnderDry(t *testing.T) {
	deps, root, sockPath := containerProject(t, "")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true, dry: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
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
	deps, root, _ := registryProject(t, `
  registry: { server: "ghcr.io", username: "acme-bot", password: "OCEL_TEST_REGISTRY_TOKEN" },`)

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	want := "REGISTRY server=ghcr.io namespace= username=acme-bot secret=true"
	if !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want %q — the push is an engine resource, so the registry it pushes to reaches the release", stdout.String(), want)
	}
}

func TestADeployThatNamesNoRegistryCarriesNone(t *testing.T) {
	deps, root, _ := registryProject(t, "")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "REGISTRY ") {
		t.Errorf("stdout = %q, want no registry on a deploy neither the project nor the provider names one for", stdout.String())
	}
}

func TestAServerlessOnlyDeployAsksForNoRegistryAtAll(t *testing.T) {
	t.Setenv("OCEL_TEST_REGISTRY_TOKEN", "hunter2")
	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "index", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	root, _ := clitest.SetUpDeployFixture(t)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", compute: "serverless", runtime: "node" }],
  registry: { server: "ghcr.io", password: "OCEL_TEST_REGISTRY_TOKEN" },
};
`)
	clitest.WriteFile(t, filepath.Join(root, "apps", "api", "src", "server.ts"), "export {};\n")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy() err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "REGISTRY ") {
		t.Errorf("stdout = %q, want a deploy that pushes no image to carry no registry token across the boundary", stdout.String())
	}
}
