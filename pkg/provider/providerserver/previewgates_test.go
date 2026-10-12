package providerserver_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var shopGate = previewgate.Gate{
	Key:           "shop-gate-key",
	PasswordMAC:   "shop-password-mac",
	BypassSecrets: []string{"shop-bypass"},
	AllowOptions:  []string{"/api/"},
}

var recordedGates = map[string]previewgate.Gate{
	"shop":  shopGate,
	"other": {Key: "other-gate-key", PasswordMAC: "other-password-mac", BypassSecrets: []string{}, AllowOptions: []string{}},
}

func seedPreviewGates(t *testing.T, vendor *fake.Provider, gates map[string]previewgate.Gate) {
	t.Helper()
	plaintext, err := json.Marshal(gates)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vendor.Cipher().Seal(t.Context(), environment.TierPreview, stackrecords.PreviewGatesAssociatedData, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := keyvalue.ReadOrEmpty(t.Context(), vendor.KeyValues(), stackrecords.NewPreviewGatesRecordKey())
	if err != nil {
		t.Fatal(err)
	}
	entry.Value = value
	if _, err := vendor.KeyValues().Write(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
}

func gatedPreviewDeployed(t *testing.T, req *contractv1.DeployRequest) *fake.Provider {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	seedPreviewGates(t, vendor, recordedGates)
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	return vendor
}

func readGateSecret(t *testing.T, worker edge.Worker, name string, into any) {
	t.Helper()
	encoded, carried := worker.Secrets[name]
	if !carried {
		t.Fatalf("Secrets has no %s, want the recorded gate keys", name)
	}
	if err := json.Unmarshal([]byte(encoded), into); err != nil {
		t.Fatalf("Secrets[%s] = %q: %v", name, encoded, err)
	}
}

func TestAPreviewDeployOnItsOwnWildcardHandsItsWorkerTheGateRecordedForItsProject(t *testing.T) {
	vendor := gatedPreviewDeployed(t, declaresPreview(previewDeployRequest(), "*.preview.shop.example"))

	var got previewgate.Gate
	readGateSecret(t, onlyStack(t, vendor).Program.Worker, edge.PreviewGateVar, &got)
	if !reflect.DeepEqual(got, shopGate) {
		t.Errorf("Secrets[%s] = %+v, want the gate recorded for shop, %+v", edge.PreviewGateVar, got, shopGate)
	}
}

func TestAPreviewDeployOfAProjectWithNoRecordedGateHandsItsWorkerNone(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	seedPreviewGates(t, vendor, map[string]previewgate.Gate{"other": recordedGates["other"]})
	if result, _ := deploy(t, client, declaresPreview(previewDeployRequest(), "*.preview.shop.example")); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if encoded, carried := onlyStack(t, vendor).Program.Worker.Secrets[edge.PreviewGateVar]; carried {
		t.Errorf("Secrets[%s] = %q, want none: no gate is recorded for shop", edge.PreviewGateVar, encoded)
	}
}

func TestAProductionDeployNeverHandsItsWorkerAGate(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	seedPreviewGates(t, vendor, recordedGates)

	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	for _, stack := range vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks() {
		if encoded, carried := stack.Program.Worker.Secrets[edge.PreviewGateVar]; carried {
			t.Errorf("Secrets[%s] = %q, want none: production serves no preview", edge.PreviewGateVar, encoded)
		}
	}
}

func TestUsePreviewWildcardHandsTheSharedEntryEveryRecordedGate(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	seedPreviewGates(t, vendor, recordedGates)

	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	specs := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Specs()
	if len(specs) != 1 {
		t.Fatalf("the edge reconciled %d wildcards, want the one raised", len(specs))
	}
	var got map[string]previewgate.Gate
	readGateSecret(t, specs[0].Program.Worker, edge.PreviewGatesVar, &got)
	if !reflect.DeepEqual(got, recordedGates) {
		t.Errorf("Secrets[%s] = %+v, want every recorded gate, %+v", edge.PreviewGatesVar, got, recordedGates)
	}
}

func gatedDirectPreviewDeployed(t *testing.T, wildcard string, hostnames ...string) *fake.Provider {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	if wildcard != "" {
		seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: wildcard, Edge: fake.KindDirect})
	}
	seedPreviewGates(t, vendor, recordedGates)
	req := declaresPreview(previewDeployRequest(), hostnames...)
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	return vendor
}

func onlyDirectStack(t *testing.T, vendor *fake.Provider) edge.StackSpec {
	t.Helper()
	stacks := vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).Stacks()
	if len(stacks) != 1 {
		t.Fatalf("the edge reconciled %d stacks, want the one this deploy provisions", len(stacks))
	}
	return stacks[0]
}

func TestAPreviewDeployOnAnEdgeThatRunsNoCodeHandsItsStackTheGateRecordedForItsProject(t *testing.T) {
	vendor := gatedDirectPreviewDeployed(t, "", "*.preview.shop.example")

	got := onlyDirectStack(t, vendor).PreviewGate
	if got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("PreviewGate = %+v, want the gate recorded for shop, %+v", got, shopGate)
	}
}

func TestAPreviewDeployOnASharedWildcardOfAnEdgeThatRunsNoCodeHandsItsStackTheGateRecordedForItsProject(t *testing.T) {
	vendor := gatedDirectPreviewDeployed(t, "preview.acme.com")

	got := onlyDirectStack(t, vendor).PreviewGate
	if got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("PreviewGate = %+v, want the gate recorded for shop, %+v", got, shopGate)
	}
}

func TestAPreviewDeployOfAProjectWithNoRecordedGateHandsItsStackNone(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedPreviewGates(t, vendor, map[string]previewgate.Gate{"other": recordedGates["other"]})
	req := declaresPreview(previewDeployRequest(), "*.preview.shop.example")
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if got := onlyDirectStack(t, vendor).PreviewGate; got != nil {
		t.Errorf("PreviewGate = %+v, want none: no gate is recorded for shop", got)
	}
}

func TestAPreviewDeployOnAnEdgeThatRunsCodeLeavesTheGateToItsProgram(t *testing.T) {
	vendor := gatedPreviewDeployed(t, declaresPreview(previewDeployRequest(), "*.preview.shop.example"))

	if got := onlyStack(t, vendor).PreviewGate; got != nil {
		t.Errorf("PreviewGate = %+v, want none: the worker secret carries the gate", got)
	}
}

func directPreviewServed(t *testing.T, gates map[string]previewgate.Gate) (contractv1connect.ProviderServiceClient, *fake.Provider, *contractv1.DeployRequest) {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).GatesPreviews(false)
	seedPreviewGates(t, vendor, gates)
	req := declaresPreview(previewDeployRequest(), "*.preview.shop.example")
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	return client, vendor, req
}

func TestAPreviewDeployWithARecordedGateIsRefusedOnAnEdgeThatCannotGateIt(t *testing.T) {
	client, vendor, req := directPreviewServed(t, recordedGates)

	result, _ := deploy(t, client, req)

	if result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want it refused: the edge would serve the gated preview to anyone")
	}
	if got := result.GetError(); !strings.Contains(got, "preview gate") || !strings.Contains(got, string(fake.KindDirect)) {
		t.Errorf("Deploy() error = %q, want it to name the preview gate and the %s edge", got, fake.KindDirect)
	}
	if stacks := vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).Stacks(); len(stacks) != 0 {
		t.Errorf("the edge reconciled %d stacks, want none: the refusal comes before anything serves the preview", len(stacks))
	}
}

func TestAPreviewDeployWithNoRecordedGateGoesAheadOnAnEdgeThatCannotGate(t *testing.T) {
	client, _, req := directPreviewServed(t, map[string]previewgate.Gate{"other": recordedGates["other"]})

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed: no gate is recorded for shop", result.GetError())
	}
}
