package providerkit_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type carrying struct{ *fake.Provider }

func (carrying) Membrane(context.Context) ([]byte, error) { return []byte(fake.Membrane), nil }

func builtRoutingApp(t *testing.T, app string, desc edge.ServeDescriptor, manifest []byte) {
	t.Helper()
	dir := filepath.Join(providerkit.ArtifactRoot(), "apps", app)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(desc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, edge.ServeDescriptorFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if manifest != nil {
		if err := os.WriteFile(filepath.Join(dir, edge.RoutingManifestFile), manifest, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTheAppPlanCarriesEveryFactTheStoodUpAppServesFrom(t *testing.T) {
	builtProject(t)
	routing := []byte(`{"routes":[{"id":"index"}]}`)
	builtRoutingApp(t, "web", edge.ServeDescriptor{EdgeRouting: true, Entry: "index", BuildID: "b1"}, routing)

	provider := carrying{fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	plans := provider.Releases().(*fake.Releaser).Plans()
	app := plans[len(plans)-1].App
	if app == nil {
		t.Fatal("the last plan the releaser saw stands up no app")
	}
	if app.AssetPrefix == "" {
		t.Error("the app plan names no asset prefix, so the stood-up app serves its static files from nowhere")
	}
	if app.Bytecode == nil || app.Bytecode.Prefix == "" {
		t.Errorf("Bytecode = %+v, want the prefix the runtime warms its compile cache under", app.Bytecode)
	}
	if app.ISR == nil || app.ISR.Prefix == "" || app.ISR.TagNamespace == "" {
		t.Errorf("ISR = %+v, want the ledger a next app revalidates through", app.ISR)
	}
	if app.Routing == nil || app.Routing.Entry != "index" || string(app.Routing.Manifest) != string(routing) {
		t.Errorf("Routing = %+v, want the entry route and manifest the build wrote", app.Routing)
	}
	if app.Membrane.Key == "" {
		t.Error("the app plan names no membrane, so the functions have nothing to boot through")
	}
	if !strings.HasPrefix(app.Membrane.Key, providerkit.MembranePrefix) {
		t.Errorf("Membrane.Key = %q, want it under %q", app.Membrane.Key, providerkit.MembranePrefix)
	}
}

func TestAProviderCarryingNoMembraneStandsAnAppUpWithout(t *testing.T) {
	builtProject(t)
	provider := fake.NewProvider(fake.Options{})
	client := servedBy(t, provider)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want a provider whose functions boot on their own to deploy all the same", result.GetError())
	}
	plans := provider.Releases().(*fake.Releaser).Plans()
	if ref := plans[len(plans)-1].App.Membrane; ref != (providerkit.ArtifactRef{}) {
		t.Errorf("Membrane = %+v where the provider carries none, want nothing placed", ref)
	}
}

func placedMembranes(t *testing.T, store providerkit.ArtifactStore) int {
	t.Helper()
	objects, holds := store.(*fake.Artifacts)
	if !holds {
		t.Fatalf("Artifacts() = %T, want the store the test reads placements back from", store)
	}
	placed := 0
	for _, ref := range objects.Keys() {
		if strings.HasPrefix(ref.Key, providerkit.MembranePrefix) {
			placed++
		}
	}
	return placed
}

func TestADryDeployPlacesNoMembrane(t *testing.T) {
	builtProject(t)
	provider := carrying{fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)

	req := deployRequest()
	req.Dry = true

	result, _ := deploy(t, client, req)
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if placed := placedMembranes(t, provider.Artifacts()); placed != 0 {
		t.Errorf("a dry deploy placed %d membranes, want a run that asks to write nothing", placed)
	}
}

func TestADeployThatIsNotDryPlacesTheMembrane(t *testing.T) {
	builtProject(t)
	provider := carrying{fake.NewProvider(fake.Options{})}
	client := servedBy(t, provider)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	if placed := placedMembranes(t, provider.Artifacts()); placed != 1 {
		t.Errorf("an applying deploy placed %d membranes, want the one its functions boot through", placed)
	}
}
