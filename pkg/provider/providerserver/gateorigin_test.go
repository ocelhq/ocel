package providerserver_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func provisionedApp(t *testing.T, vendor *fake.Provider) *provider.AppSpec {
	t.Helper()
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App != nil {
			return spec.App
		}
	}
	t.Fatal("the deploy provisioned no app")
	return nil
}

func directEdge(vendor *fake.Provider) *fake.Edge {
	return vendor.Edges().(*fake.Edges).Edge(fake.KindDirect)
}

func originGatedPreviewServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider, *contractv1.DeployRequest) {
	t.Helper()
	client, vendor, req := directPreviewServed(t, recordedGates)
	directEdge(vendor).Gates(true)
	directEdge(vendor).GatesAtOrigin(true)
	return client, vendor, req
}

func protectedOriginGatedPreview(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	client, vendor, req := originGatedPreviewServed(t)
	if result, _ := deploy(t, client, protecting(req)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	return client, vendor
}

func TestAProtectedPreviewDeployDoesNotCountItsOwnPreviewsFunctionsIgnoringTheGate(t *testing.T) {
	client, vendor, req := originGatedPreviewServed(t)
	previewIgnoringTheGate(t, vendor, "pr-7")

	result, events := deploy(t, client, protecting(req))

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	for _, text := range eventTexts(t, events, result) {
		if strings.Contains(text, "of preview pr-7") {
			t.Errorf("the deploy said %q, and the preview it deploys replaces what pr-7 recorded: a protected deploy refuses a function that ignores the gate", text)
		}
	}
	if note := noteOf(result); strings.Contains(note, "pr-7") {
		t.Errorf("the result's notes = %q, want none naming the preview this deploy replaces", note)
	}
}

func TestTheStatusOfAProjectWithProtectionOffSaysNothingOfFunctionsThatIgnoreTheGate(t *testing.T) {
	client, vendor, req := originGatedPreviewServed(t)
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	previewIgnoringTheGate(t, vendor, "pr-3")

	edges := getProtectionThrough(t, client, fake.KindDirect, false).GetEdges()

	if len(edges) != 1 || edges[0].GetError() != "" {
		t.Errorf("status edges = %v, want the edge reported plainly: with protection off no preview is meant to be gated", edges)
	}
}

func TestFunctionsIgnoringTheGateDoNotMarkAProjectWhoseEdgeGatesItselfNotLive(t *testing.T) {
	client, vendor := protectedPreview(t)
	previewIgnoringTheGate(t, vendor, "pr-3")

	edges := getProtection(t, client, false).GetEdges()
	result, _, err := publishProtection(t, client)

	if len(edges) != 1 || !edges[0].GetPublished() || edges[0].GetError() != "" {
		t.Errorf("status edges = %v, want the edge live: it gates every request before any function sees it", edges)
	}
	if err != nil || !result.GetSuccess() {
		t.Errorf("PublishProtection() = %+v, %v, want it to succeed: the edge gates pr-3 ahead of its functions", result, err)
	}
}

func TestAnOpenPreviewDeployDisablesTheRecordBeforeItTakesTheEdgesGateDown(t *testing.T) {
	client, vendor := unprotectedPreviewServed(t)
	var enabledAtReconcile []bool
	relayEdge(vendor).OnReconcile(func(spec edge.StackSpec) {
		record, _ := recordedGateOf(t, vendor, "shop")
		if spec.Gate == nil {
			enabledAtReconcile = append(enabledAtReconcile, record.Enabled)
		}
	})

	if result, _ := deploy(t, client, previewDeployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if len(enabledAtReconcile) == 0 {
		t.Fatal("the open deploy never handed the edge its withdrawal")
	}
	for _, enabled := range enabledAtReconcile {
		if enabled {
			t.Error("the edge was handed no gate while shop's record still said its previews are protected: a deploy that stopped there would leave them open with status calling them protected")
		}
	}
}

func TestAnOpenPreviewDeployThatDeployedFunctionsIgnoringTheGateWarnsWhenProtectionWasTurnedOnMeanwhile(t *testing.T) {
	client, vendor, req := originGatedPreviewServed(t)
	vendor.FakeStacks().IgnoreGate()
	vendor.FakeStacks().Entering(func(spec provider.StackSpec) error {
		if spec.App == nil {
			return nil
		}
		return stackrecords.ChangeGates(t.Context(), vendor.KeyValues(), vendor.Cipher(), environment.TierPreview, func(records map[string]stackrecords.GateRecord) error {
			record := records["shop"]
			record.Enabled = true
			records["shop"] = record
			return nil
		})
	})

	result, events := deploy(t, client, req)

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	warned := false
	for _, text := range eventTexts(t, events, result) {
		warned = warned || (strings.Contains(text, "protection.preview") && strings.Contains(text, "of preview pr-7"))
	}
	if !warned {
		t.Errorf("the deploy left functions of pr-7 that ignore the gate answering anyone after protection was turned on, without saying so: %q", eventTexts(t, events, result))
	}
	if note := noteOf(result); !strings.Contains(note, "pr-7") {
		t.Errorf("the result's notes = %q, want them to name preview pr-7", note)
	}
}

func TestAProtectedPreviewDeployOnAnEdgeThatGatesAtTheOriginHandsTheAppTheGate(t *testing.T) {
	client, vendor, req := directPreviewServed(t, recordedGates)
	vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).Gates(true)
	vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).GatesAtOrigin(true)

	if result, _ := deploy(t, client, protecting(req)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	got := provisionedApp(t, vendor).Gate
	if got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("AppSpec.Gate = %+v, want the gate recorded for shop, %+v: the origin enforces it", got, shopGate)
	}
}

func TestAProtectedPreviewDeployIsRefusedWhereTheEdgeLeavesTheGateToAnOriginThatRunsNone(t *testing.T) {
	client, vendor, req := directPreviewServed(t, recordedGates)
	vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).Gates(true)
	vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).GatesAtOrigin(true)
	vendor.WithFacts(func(facts *provider.Facts) { facts.RunsGates = false })

	result, _ := deploy(t, client, protecting(req))

	if result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want it refused: the origin would ignore the gate and serve the protected preview to anyone")
	}
	if got := result.GetError(); !strings.Contains(got, "protection.preview") {
		t.Errorf("Deploy() error = %q, want it to name protection.preview", got)
	}
	for _, spec := range vendor.FakeStacks().Provisioned() {
		if spec.App != nil {
			t.Errorf("the refused deploy provisioned app %s", spec.App.App)
		}
	}
}

func TestAProtectedPreviewDeployHandsTheEdgeTheRecordedGateToPublishToItsOrigin(t *testing.T) {
	client, vendor, req := directPreviewServed(t, recordedGates)
	front := vendor.Edges().(*fake.Edges).Edge(fake.KindDirect)
	front.Gates(true)
	front.GatesAtOrigin(true)
	var reconciled []*gate.Gate
	front.OnReconcile(func(spec edge.StackSpec) { reconciled = append(reconciled, spec.Gate) })

	if result, _ := deploy(t, client, protecting(req)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if len(reconciled) == 0 {
		t.Fatal("the deploy never reconciled the edge")
	}
	for _, got := range reconciled {
		if got == nil || !reflect.DeepEqual(*got, shopGate) {
			t.Errorf("the edge reconciled with gate %+v, want the gate recorded for shop: an edge handed none withdraws the gate and opens the preview", got)
		}
	}
}

func TestAProtectedPreviewDeployOnAnEdgeThatGatesItselfHandsTheAppNoGate(t *testing.T) {
	client, vendor, req := directPreviewServed(t, recordedGates)
	vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).Gates(true)

	if result, _ := deploy(t, client, protecting(req)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	if got := provisionedApp(t, vendor).Gate; got != nil {
		t.Errorf("AppSpec.Gate = %+v, want none: the edge gates and the origin never holds the keys", got)
	}
}
