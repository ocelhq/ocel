package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func setUpProviderFixture(t *testing.T, options string) (root, journal string, deps cmddeps.Deps) {
	t.Helper()

	root, _ = clitest.SetUpDeployFixture(t)
	clitest.WriteUsageMonorepo(t, root)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: `+options+` },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "express" }],
};
`)

	journal = filepath.Join(t.TempDir(), "configure.journal")
	t.Setenv(clitest.FakeConfigureJournalEnvVar, journal)

	deps = newDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []manifestbuilder.Function{
		{Route: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
	})
	return root, journal, deps
}

func TestDeployConfiguresTheProviderOnceAtSessionSetup(t *testing.T) {
	root, journal, deps := setUpProviderFixture(t, `{ region: "eu-west-2", transforms: ["./infra/net.transform.ts"], certificates: { "app.acme.com": "arn:aws:acm:eu-west-2:1:certificate/x" } }`)

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := clitest.ReadJournal(t, journal)
	if len(got) != 1 {
		t.Fatalf("the provider was configured %d times, want exactly 1 for the session: %v", len(got), got)
	}
	want := "region=eu-west-2 transforms=./infra/net.transform.ts certificates=map[app.acme.com:arn:aws:acm:eu-west-2:1:certificate/x]"
	if got[0] != want {
		t.Errorf("provider saw %q, want %q", got[0], want)
	}
}

func TestDeployRendersTheProviderRefusalAgainstTheConfigFile(t *testing.T) {
	root, _, deps := setUpProviderFixture(t, `{ regionn: "eu-west-2" }`)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want options the provider refuses reported; stdout=%s", stdout.String())
	}
	rendered := stdout.String() + stderr.String()
	for _, want := range []string{
		projectconfig.ConfigFileName + " configures @ocel/provider-aws with options it does not accept",
		`"regionn"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output = %q, want it to contain %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "invalid_argument:") {
		t.Errorf("rendered output = %q, want no raw connect code prefix", rendered)
	}
}
