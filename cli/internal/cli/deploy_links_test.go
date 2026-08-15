package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
)

func writeLinkedMonorepo(t *testing.T, root string, links string) {
	t.Helper()

	writeUsageMonorepo(t, root)
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: {} },
  domains: { preview: "*.preview.acme.com" },
  links: [`+links+`],
  apps: [{ name: "api", path: "apps/api", framework: "express" }],
};
`)
}

func deployLinked(t *testing.T, links string) (root string, stdout, stderr bytes.Buffer, err error) {
	t.Helper()

	d := defaultDeps()
	setLoggedIn(&d)
	stubAppFunctions(&d, []manifestbuilder.Function{
		{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
	})
	root, _ = setUpDeployFixture(t)
	writeLinkedMonorepo(t, root, links)

	err = runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	return root, stdout, stderr, err
}

func TestDeployBindsListedLinks(t *testing.T) {
	t.Run("a listed resource reaches the provider bound to its published record", func(t *testing.T) {
		t.Setenv(fakePublishedLinksEnvVar, "main")

		_, stdout, stderr, err := deployLinked(t, `"main"`)
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		if !strings.Contains(out, "LINK bound=db--main name=main") {
			t.Errorf("stdout = %q, want the `links` binding to have reached the provider on the manifest", out)
		}
		if !strings.Contains(out, "USAGE app=api resource=db--main") {
			t.Errorf("stdout = %q, want a linked resource to carry its usage edge like any other", out)
		}
	})

	t.Run("a listed resource nothing published refuses the deploy by name", func(t *testing.T) {
		t.Setenv(fakePublishedLinksEnvVar, "")

		_, stdout, stderr, err := deployLinked(t, `"main"`)
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "published a link named main") {
			t.Errorf("output = %q, want the refusal to name the link that was never published", combined)
		}
	})

	t.Run("a published name this project provisions instead is called out", func(t *testing.T) {
		t.Setenv(fakePublishedLinksEnvVar, "main")

		_, stdout, stderr, err := deployLinked(t, ``)
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "LINK shadowed=db--main name=main") {
			t.Errorf("stdout = %q, want the collision between a provisioned resource and a published link surfaced", out)
		}
	})

	t.Run("a listed name nothing declares refuses before any provider is reached", func(t *testing.T) {
		_, stdout, stderr, err := deployLinked(t, `"nowhere"`)
		if err == nil {
			t.Fatalf("runDeploy err = nil, want the deploy refused; stdout=%s", stdout.String())
		}
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "nowhere") {
			t.Errorf("output = %q, want the unbound link named", combined)
		}
	})
}
