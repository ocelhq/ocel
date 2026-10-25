package deploy

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/environment"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

const originDefinitions = `[{"key":"ORIGIN_CERT","class":"VARIABLE_CLASS_PLAIN","required":true},{"key":"ORIGIN_KEY","class":"VARIABLE_CLASS_SECRET","required":true}]`

const originBindings = `  apps: [{ name: "api", path: "apps/api", framework: "node", compute: "serverless" }],
  bindings: { variables: { ORIGIN_CERT: "@origin.certificate", ORIGIN_KEY: "@origin.key" } },
`

func originProject(t *testing.T) (Dependencies, clitest.FakeProject) {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	fixture := setUpVariablesProject(t, originDefinitions)
	writeConfig(t, fixture.Root, originBindings)
	writeAppSource(t, fixture.Root, "api")
	return dependencies, fixture
}

func publishOrigin(t *testing.T, fixture clitest.FakeProject) {
	t.Helper()
	custom, err := structpb.NewStruct(map[string]any{"certificate": "-----BEGIN CERTIFICATE-----", "key": "-----BEGIN PRIVATE KEY-----"})
	if err != nil {
		t.Fatal(err)
	}
	published := &variablestorev1.PublishedBinding{
		Binding: &bindingsv1.Binding{Name: "origin", Source: "pulumi", Properties: &bindingsv1.Binding_Custom{Custom: custom}},
		Secrets: []string{"key"},
	}
	pair, err := variablestoreserver.BindingPair("urn:pulumi:cloud", published)
	if err != nil {
		t.Fatal(err)
	}
	scope := variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction}
	if _, err := valueStore(fixture).SetBinding(context.Background(), scope, "", "urn:pulumi:cloud", "origin", pair); err != nil {
		t.Fatalf("SetBinding: %v", err)
	}
}

func TestADeployHandsAnAppThePlainValueAndTheSecretAPulumiProgramPublished(t *testing.T) {
	dependencies, fixture := originProject(t)
	publishOrigin(t, fixture)

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}
	if strings.Contains(out, "PRIVATE KEY") {
		t.Errorf("output = %q, want the published secret never in it", out)
	}

	var values provider.AppValues
	for _, spec := range fixture.Provider.FakeStacks().Provisioned() {
		if spec.App != nil {
			values = spec.App.Values
		}
	}
	if values.Plain["ORIGIN_CERT"] != "-----BEGIN CERTIFICATE-----" {
		t.Errorf("the app is handed plain %v, want ORIGIN_CERT read off the published record", values.Plain)
	}
	if !slices.Contains(values.Secrets, provider.SecretRef{Key: "ORIGIN_KEY", Binding: "origin", Property: "key"}) {
		t.Errorf("the app is handed secrets %+v, want ORIGIN_KEY read live from origin's key", values.Secrets)
	}
	reader := variablestore.EnvironmentReader{
		KeyValues: fixture.Provider.KeyValues(),
		Cipher:    fixture.Provider.Cipher(),
		Scope:     variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction},
	}
	read, _, err := live.Fetch(context.Background(), reader, nil, values.LiveBindings())
	if err != nil {
		t.Fatalf("live.Fetch: %v", err)
	}
	if read["ORIGIN_KEY"] != "-----BEGIN PRIVATE KEY-----" {
		t.Errorf("the runtime reads ORIGIN_KEY = %q, want the published secret", read["ORIGIN_KEY"])
	}
}

func TestADeployRefusesABoundVariableAValueWasAlsoSetFor(t *testing.T) {
	dependencies, fixture := originProject(t)
	publishOrigin(t, fixture)
	envSet(t, fixture, "ORIGIN_CERT", "set by hand", envOptions{})

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err == nil {
		t.Fatal("runDeploy err = nil, want the deploy refused")
	}
	for _, want := range []string{"ORIGIN_CERT", "@origin.certificate", "ocel env rm ORIGIN_CERT"} {
		if !strings.Contains(out+err.Error(), want) {
			t.Errorf("output = %q, err = %v, want it to name %q", out, err, want)
		}
	}
	if sent := sentDeploys(t, fixture); len(sent) != 0 {
		t.Errorf("the CLI sent %d deploys, want none", len(sent))
	}
}

func TestADeployRefusesABoundVariableNothingPublished(t *testing.T) {
	dependencies, fixture := originProject(t)

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err == nil {
		t.Fatal("runDeploy err = nil, want the deploy refused")
	}
	if !strings.Contains(out+err.Error(), `"origin"`) {
		t.Errorf("output = %q, err = %v, want it to name the record nothing published", out, err)
	}
}
