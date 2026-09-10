package deploy

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func writeBoundMonorepo(t *testing.T, root string, bindings string) {
	t.Helper()

	clitest.WriteUsageMonorepo(t, root)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { name: "aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  bindings: {`+bindings+`},
  apps: [{ name: "api", path: "apps/api", framework: "node" }],
};
`)
}

func deployBound(t *testing.T, bindings string) (root string, stdout, stderr bytes.Buffer, err error) {
	t.Helper()

	deps := clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "api", Runtime: manifestbuilder.Runtime{Name: "node"}, Handler: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	root, _ = clitest.SetUpDeployFixture(t)
	writeBoundMonorepo(t, root, bindings)

	err = runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	return root, stdout, stderr, err
}

func TestDeployBindsListedBindings(t *testing.T) {
	t.Run("a listed resource reaches the provider bound to its published record", func(t *testing.T) {
		t.Setenv(clitest.FakePublishedBindingsEnvVar, "main")

		_, stdout, stderr, err := deployBound(t, `postgres: { main: "main" }`)
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "BINDING bound=db--main name=main") {
			t.Errorf("stdout = %q, want the `bindings` binding to have reached the provider on the manifest", out)
		}
		if !strings.Contains(out, "USAGE app=api resource=db--main") {
			t.Errorf("stdout = %q, want a bound resource to carry its usage edge like any other", out)
		}
	})

	t.Run("a listed resource nothing published refuses the deploy by name", func(t *testing.T) {
		t.Setenv(clitest.FakePublishedBindingsEnvVar, "")

		_, stdout, stderr, err := deployBound(t, `postgres: { main: "main" }`)
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "published a binding named main") {
			t.Errorf("output = %q, want the refusal to name the binding that was never published", combined)
		}
	})

	t.Run("a published name this project provisions instead is called out", func(t *testing.T) {
		t.Setenv(clitest.FakePublishedBindingsEnvVar, "main")

		_, stdout, stderr, err := deployBound(t, ``)
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "BINDING shadowed=db--main name=main") {
			t.Errorf("stdout = %q, want the collision between a provisioned resource and a published binding surfaced", out)
		}
	})

	t.Run("a listed name nothing declares refuses before any provider is reached", func(t *testing.T) {
		_, stdout, stderr, err := deployBound(t, `postgres: { nowhere: "nowhere" }`)
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "nowhere") {
			t.Errorf("output = %q, want the unbound binding named", combined)
		}
	})
}
