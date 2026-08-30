package deploy

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
)

func containerProject(t *testing.T, health string) (cmddeps.Deps, string, string) {
	t.Helper()

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "index", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
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
