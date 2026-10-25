package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/ocelhq/ocel/pkg/environment"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

func publishCustom(t *testing.T, vendor *fake.Provider, name string, properties map[string]any, secrets ...string) {
	t.Helper()
	custom, err := structpb.NewStruct(properties)
	if err != nil {
		t.Fatal(err)
	}
	published := &variablestorev1.PublishedBinding{
		Binding: &bindingsv1.Binding{Name: name, Source: "pulumi", Properties: &bindingsv1.Binding_Custom{Custom: custom}},
		Secrets: secrets,
	}
	if err := variablestoreserver.VerifyBinding(published); err != nil {
		t.Fatalf("VerifyBinding: %v", err)
	}
	pair, err := variablestoreserver.BindingPair("urn:pulumi:cloud", published)
	if err != nil {
		t.Fatalf("BindingPair: %v", err)
	}
	store := variablestore.Store{KeyValues: vendor.KeyValues(), Cipher: vendor.Cipher()}
	scope := variablestore.Scope{Project: "shop", Tier: environment.TierProduction}
	if _, err := store.SetBindings(context.Background(), scope, "", "urn:pulumi:cloud", []variablestore.NamedBindingWrite{{Name: name, Write: pair}}); err != nil {
		t.Fatalf("SetBindings: %v", err)
	}
}

func publishOrigin(t *testing.T, vendor *fake.Provider) {
	t.Helper()
	publishCustom(t, vendor, "origin", map[string]any{"certificate": "-----BEGIN CERTIFICATE-----", "key": "-----BEGIN PRIVATE KEY-----", "port": 8443.0}, "key")
}

func bindingVariable(req *contractv1.DeployRequest, class resourcesv1.VariableClass, key, binding, property string) *contractv1.DeployRequest {
	req.Manifest.Apps[0].Variables = append(req.Manifest.Apps[0].Variables, &contractv1.ManifestVariable{
		Key: key, Class: class, Binding: binding, Property: property,
	})
	return req
}

func provisionedValues(t *testing.T, vendor *fake.Provider, app string) provider.AppValues {
	t.Helper()
	specs := vendor.FakeStacks().Provisioned()
	for i := len(specs) - 1; i >= 0; i-- {
		if specs[i].App != nil && specs[i].App.App == app {
			return specs[i].App.Values
		}
	}
	t.Fatalf("no plan the stacks port saw provisions %s", app)
	return provider.AppValues{}
}

func TestDeployHandsAnAppAPublishedPlainValueAndAPublishedSecretAsItsDeclaredVariables(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	publishOrigin(t, vendor)

	req := deployRequest()
	bindingVariable(req, resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "ORIGIN_CERT", "origin", "certificate")
	bindingVariable(req, resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE, "ORIGIN_PORT", "origin", "port")
	bindingVariable(req, resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "ORIGIN_KEY", "origin", "key")
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	values := provisionedValues(t, vendor, "web")
	if values.Plain["ORIGIN_CERT"] != "-----BEGIN CERTIFICATE-----" || values.Sensitive["ORIGIN_PORT"] != "8443" {
		t.Errorf("web is handed plain %v and sensitive %v, want the certificate and the port read off the published record", values.Plain, values.Sensitive)
	}
	if !slices.Contains(values.Secrets, provider.SecretRef{Key: "ORIGIN_KEY", Binding: "origin", Property: "key"}) {
		t.Errorf("web is handed secrets %+v, want ORIGIN_KEY read live from origin's key", values.Secrets)
	}
	for _, resolved := range []map[string]string{values.Plain, values.Sensitive, values.ContainerEnv} {
		for key, value := range resolved {
			if strings.Contains(value, "PRIVATE KEY") {
				t.Errorf("%s carries the secret's plaintext, want a secret resolved only by the runtime", key)
			}
		}
	}

	reader := variablestore.EnvironmentReader{
		KeyValues: vendor.KeyValues(),
		Cipher:    vendor.Cipher(),
		Scope:     variablestore.Scope{Project: "shop", Tier: environment.TierProduction},
	}
	read, _, err := live.Fetch(context.Background(), reader, nil, values.LiveBindings())
	if err != nil {
		t.Fatalf("live.Fetch: %v", err)
	}
	if read["ORIGIN_KEY"] != "-----BEGIN PRIVATE KEY-----" {
		t.Errorf("the runtime reads ORIGIN_KEY = %q, want the published secret", read["ORIGIN_KEY"])
	}
	if err := live.Conform(values.LiveBindings(), read); err != nil {
		t.Errorf("Conform = %v, want what the runtime read to conform", err)
	}
}

func TestDeployRefusesAVariableBoundToARecordNothingPublished(t *testing.T) {
	req := bindingVariable(deployRequest(), resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "ORIGIN_CERT", "origin", "certificate")
	message := refusedDeploy(t, req, nil)
	for _, want := range []string{`"origin"`, "Nothing at all is published"} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal = %q, want it to contain %q", message, want)
		}
	}
}

func TestDeployRefusesAVariableBoundToAPropertyTheRecordDoesNotPublish(t *testing.T) {
	req := bindingVariable(deployRequest(), resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "ORIGIN_CHAIN", "origin", "chain")
	message := refusedDeploy(t, req, func(p *fake.Provider) { publishOrigin(t, p) })
	for _, want := range []string{"ORIGIN_CHAIN", "@origin.chain", "certificate, key, port"} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal = %q, want it to contain %q", message, want)
		}
	}
}

func TestDeployRefusesAVariableBoundToATypedRecord(t *testing.T) {
	req := bindingVariable(deployRequest(), resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, "ORDERS_HOST", "orders-db", "host")
	message := refusedDeploy(t, req, func(p *fake.Provider) {
		publishRecord(t, p, environment.TierProduction, "terraform", postgresRecord("orders-db", "terraform"))
	})
	for _, want := range []string{"ORDERS_HOST", "postgres", "custom"} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal = %q, want it to contain %q", message, want)
		}
	}
}

func TestDeployRefusesAPublishedSecretForAVariableThatIsNotSecret(t *testing.T) {
	for _, class := range []resourcesv1.VariableClass{resourcesv1.VariableClass_VARIABLE_CLASS_PLAIN, resourcesv1.VariableClass_VARIABLE_CLASS_SENSITIVE} {
		req := bindingVariable(deployRequest(), class, "ORIGIN_KEY", "origin", "key")
		message := refusedDeploy(t, req, func(p *fake.Provider) { publishOrigin(t, p) })
		for _, want := range []string{"ORIGIN_KEY", "secret"} {
			if !strings.Contains(message, want) {
				t.Errorf("%s: refusal = %q, want it to contain %q", class, message, want)
			}
		}
		if strings.Contains(message, "PRIVATE KEY") {
			t.Errorf("refusal = %q, want the secret's value never in it", message)
		}
	}
}

func TestDeployRefusesAPublishedPlainValueForASecretVariable(t *testing.T) {
	req := bindingVariable(deployRequest(), resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "ORIGIN_CERT", "origin", "certificate")
	message := refusedDeploy(t, req, func(p *fake.Provider) { publishOrigin(t, p) })
	for _, want := range []string{"ORIGIN_CERT", "@origin.certificate", "secret"} {
		if !strings.Contains(message, want) {
			t.Errorf("refusal = %q, want it to contain %q", message, want)
		}
	}
}

func TestABoundSecretIsNotRefusedAsUnsetByAWrappingProvidersContainerApp(t *testing.T) {
	req := namingARegistry(containerDeployRequest("/healthz"))
	bindingVariable(req, resourcesv1.VariableClass_VARIABLE_CLASS_SECRET, "ORIGIN_KEY", "origin", "key")

	delivered := deliveredByWrapping(t, req, func(p *fake.Provider) { publishOrigin(t, p) })

	if got, ok := delivered["ORIGIN_KEY"]; ok {
		t.Errorf("a container is handed ORIGIN_KEY=%q, want the runtime inside it to read the secret", got)
	}
}
