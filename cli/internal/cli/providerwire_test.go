package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
)

func setUpProviderFixture(t *testing.T, options string) (root, journal string, d deps) {
	t.Helper()

	root, _ = setUpDeployFixture(t)
	writeUsageMonorepo(t, root)
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default {
  slug: "test-app",
  provider: { package: "@ocel/provider-aws", options: `+options+` },
  domains: { preview: "*.preview.acme.com" },
  apps: [{ name: "api", path: "apps/api", framework: "express" }],
};
`)

	journal = filepath.Join(t.TempDir(), "configure.journal")
	t.Setenv(fakeConfigureJournalEnvVar, journal)

	d = defaultDeps()
	setLoggedIn(&d)
	stubAppFunctions(&d, []manifestbuilder.Function{
		{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
	})
	return root, journal, d
}

func TestDeployConfiguresTheProviderOnceAtSessionSetup(t *testing.T) {
	root, journal, d := setUpProviderFixture(t, `{ region: "eu-west-2", transforms: ["./infra/net.transform.ts"], certificates: { "app.acme.com": "arn:aws:acm:eu-west-2:1:certificate/x" } }`)

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	got := readJournal(t, journal)
	if len(got) != 1 {
		t.Fatalf("the provider was configured %d times, want exactly 1 for the session: %v", len(got), got)
	}
	want := "region=eu-west-2 transforms=./infra/net.transform.ts certificates=map[app.acme.com:arn:aws:acm:eu-west-2:1:certificate/x]"
	if got[0] != want {
		t.Errorf("provider saw %q, want %q", got[0], want)
	}
}

func TestDeployRendersTheProviderRefusalAgainstTheConfigFile(t *testing.T) {
	root, _, d := setUpProviderFixture(t, `{ regionn: "eu-west-2" }`)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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

func TestProviderConfigCarriesTheDescriptorOptionsOpaquely(t *testing.T) {
	config, err := providerConfig(&projectconfig.ProviderDescriptor{
		Package: "@ocel/provider-aws",
		Options: json.RawMessage(`{"region":"us-east-1"}`),
	})
	if err != nil {
		t.Fatalf("providerConfig: %v", err)
	}
	if got := config.GetOptions().GetFields()["region"].GetStringValue(); got != "us-east-1" {
		t.Errorf("region = %q, want us-east-1", got)
	}
}

func TestProviderConfigRefusesOptionsThatAreNotAJSONObject(t *testing.T) {
	_, err := providerConfig(&projectconfig.ProviderDescriptor{
		Package: "@ocel/provider-aws",
		Options: json.RawMessage(`["us-east-1"]`),
	})
	if err == nil {
		t.Fatal("providerConfig err = nil, want a non-object options value refused")
	}
	if !strings.Contains(err.Error(), "not a JSON object") {
		t.Errorf("err = %v, want it to say the options are not a JSON object", err)
	}
}

func TestProviderConfigLeavesAnUnconfiguredProviderWithoutOptions(t *testing.T) {
	config, err := providerConfig(&projectconfig.ProviderDescriptor{
		Package: "@ocel/provider-aws",
		Options: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("providerConfig: %v", err)
	}
	if len(config.GetOptions().GetFields()) != 0 {
		t.Errorf("options = %v, want none for a descriptor carrying no options", config.GetOptions())
	}
}
