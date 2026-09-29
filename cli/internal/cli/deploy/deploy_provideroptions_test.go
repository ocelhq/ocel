package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/pkg/appbuild"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode %v: %v", value, err)
	}
	return string(encoded)
}

func setUpProviderFixture(t *testing.T, options string) (root, journal string, deps cmddeps.Deps) {
	t.Helper()
	return setUpProviderFixtureWith(t, options, nil)
}

func setUpProviderFixtureWith(t *testing.T, options string, transforms []string) (root, journal string, deps cmddeps.Deps) {
	t.Helper()

	root, _ = clitest.SetUpDeployFixture(t)
	clitest.WriteUsageMonorepo(t, root)
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  transforms: `+mustJSON(t, transforms)+`,
  provider: { fake: `+options+` },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "node" }],
};
`)

	journal = filepath.Join(t.TempDir(), "configure.journal")
	t.Setenv(clitest.FakeConfigureJournalEnvVar, journal)

	deps = clitest.NewDeps()
	clitest.SetLoggedIn(&deps)
	clitest.StubBuild(&deps, []build.Function{
		{Route: "api", Framework: appbuild.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: "api"},
	})
	return root, journal, deps
}

func TestDeployConfiguresTheProviderOnceAtSessionSetup(t *testing.T) {
	root, journal, deps := setUpProviderFixtureWith(t,
		`{ location: "zone-b", certificates: { "app.acme.com": "fake-certificate/x" } }`,
		[]string{"./transforms/net.transform.ts"})

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stdout)
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := clitest.ReadJournal(t, journal)
	if len(got) != 1 {
		t.Fatalf("the provider was configured %d times, want exactly 1 for the session: %v", len(got), got)
	}
	want := "location=zone-b transforms=./transforms/net.transform.ts certificates=map[app.acme.com:fake-certificate/x]"
	if got[0] != want {
		t.Errorf("provider saw %q, want %q", got[0], want)
	}
}

func TestDeployRendersTheProviderRefusalAgainstTheConfigFile(t *testing.T) {
	root, _, deps := setUpProviderFixture(t, `{ locationn: "zone-b" }`)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(deps, &stdout)
	err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want options the provider refuses reported; stdout=%s", stdout.String())
	}
	rendered := stdout.String() + stderr.String()
	for _, want := range []string{
		`configures provider "fake" with options it does not accept`,
		`"provider.fake.locationn"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output = %q, want it to contain %q", rendered, want)
		}
	}
	if strings.Contains(rendered, "invalid_argument:") {
		t.Errorf("rendered output = %q, want no raw connect code prefix", rendered)
	}
}
