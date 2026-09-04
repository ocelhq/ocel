package deploy

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
)

func fixtureWithAnApp(t *testing.T) (root, sockPath string, deps cmddeps.Deps) {
	t.Helper()

	deps = clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
	})
	root, sockPath = clitest.SetUpDeployFixture(t)
	addAppToFixtureConfig(t, root)
	return root, sockPath, deps
}

func TestDeploySuppressingResources(t *testing.T) {
	t.Run("the fixture's declaration reaches the manifest by default", func(t *testing.T) {
		root, sockPath, deps := fixtureWithAnApp(t)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "RESOURCE name=main") {
			t.Errorf("stdout = %q, want the discovered resource in the manifest", out)
		}
		if strings.Contains(out, "suppress_resources=true") {
			t.Errorf("stdout = %q, want a deploy that provisions to say nothing about suppression", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})

	t.Run("the env var drops every declaration and tells the provider so", func(t *testing.T) {
		root, sockPath, deps := fixtureWithAnApp(t)
		t.Setenv(suppressResourcesEnvVar, "1")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if strings.Contains(out, "RESOURCE ") {
			t.Errorf("stdout = %q, want no resource in the manifest", out)
		}
		if !strings.Contains(out, "suppress_resources=true") {
			t.Errorf("stdout = %q, want the request to carry the suppression", out)
		}
		if !strings.Contains(out, "Deployed") {
			t.Errorf("stdout = %q, want the app to deploy with nothing provisioned", out)
		}

		clitest.WaitForNoStaleSocket(t, sockPath)
	})
}
