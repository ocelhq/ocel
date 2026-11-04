package providerserver_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func productionDeployRequest() *contractv1.DeployRequest {
	req := deployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	return req
}

func productionServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	builtProject(t)
	return deployServed(t)
}

func TestAProtectedProductionDeployCreatesItsOwnGateAndHandsItToItsStack(t *testing.T) {
	client, vendor := productionServed(t)

	result, events := deploy(t, client, protecting(productionDeployRequest()))

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	record, held := recordedGateIn(t, vendor, environment.TierProduction, "shop")
	if !held || !record.Enabled || len(record.Password) < 12 {
		t.Fatalf("the production record is %+v, held %t, want an enabled gate with a generated password", record, held)
	}
	if got := onlyStack(t, vendor).Gate; got == nil || !reflect.DeepEqual(*got, record.Gate) {
		t.Errorf("Gate = %+v, want the production gate %+v", got, record.Gate)
	}
	if _, held := recordedGateOf(t, vendor, "shop"); held {
		t.Error("a production deploy recorded a preview gate")
	}
	for _, text := range eventTexts(t, events, result) {
		if strings.Contains(text, record.Password) || strings.Contains(text, record.Gate.Key) {
			t.Errorf("the deploy said %q, and the password and key must never be shown", text)
		}
	}
}

func TestAProductionGateIsNeverTheGateOfThePreviewsOfTheSameProject(t *testing.T) {
	client, vendor := productionServed(t)
	seedGates(t, vendor, recordedGates)

	if result, _ := deploy(t, client, protecting(productionDeployRequest())); !result.GetSuccess() {
		t.Fatal("Deploy() failed")
	}

	production, held := recordedGateIn(t, vendor, environment.TierProduction, "shop")
	if !held || production.Password == shopPassword || production.Gate.Key == shopGate.Key {
		t.Errorf("the production record is %+v, held %t, want credentials of its own: the preview password must never open production", production, held)
	}
	if got := onlyStack(t, vendor).Gate; got == nil || got.Key == shopGate.Key {
		t.Errorf("the production stack was handed %+v, want not the preview gate", got)
	}
	if preview, _ := recordedGateOf(t, vendor, "shop"); !preview.Enabled || preview.Password != shopPassword {
		t.Errorf("the preview record is %+v after a production deploy, want it untouched", preview)
	}
}

func TestAProductionDeployThatDoesNotProtectOpensProductionKeepsItsCredentialsAndLeavesPreviewsProtected(t *testing.T) {
	client, vendor := productionServed(t)
	seedGatesIn(t, vendor, environment.TierProduction, recordedGates)
	seedGates(t, vendor, recordedGates)

	result, events := deploy(t, client, productionDeployRequest())

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	production, _ := recordedGateIn(t, vendor, environment.TierProduction, "shop")
	if production.Enabled || production.Password != shopPassword || production.Gate.Key != shopGate.Key {
		t.Errorf("the production record is %+v, want it disabled with its credentials kept", production)
	}
	if got := onlyStack(t, vendor).Gate; got != nil {
		t.Errorf("Gate = %+v, want none: the edge must take its production copy down", got)
	}
	if preview, _ := recordedGateOf(t, vendor, "shop"); !preview.Enabled {
		t.Error("a production deploy opened the previews")
	}
	warned := false
	for _, text := range eventTexts(t, events, result) {
		warned = warned || (strings.Contains(text, "protection.production") && strings.Contains(text, "opens the production of shop"))
	}
	if !warned {
		t.Error("the deploy opened production without saying so and naming protection.production")
	}
}

func TestTurningProductionProtectionBackOnRestoresTheSameCredentials(t *testing.T) {
	client, vendor := productionServed(t)
	seedGatesIn(t, vendor, environment.TierProduction, recordedGates)
	if result, _ := deploy(t, client, productionDeployRequest()); !result.GetSuccess() {
		t.Fatal("unprotected Deploy() failed")
	}

	if result, _ := deploy(t, client, protecting(productionDeployRequest())); !result.GetSuccess() {
		t.Fatal("protected Deploy() failed")
	}

	record, _ := recordedGateIn(t, vendor, environment.TierProduction, "shop")
	if !record.Enabled || record.Password != shopPassword || record.Gate.Key != shopGate.Key {
		t.Errorf("the production record is %+v, want it enabled again with the same password and key", record)
	}
}

func TestAProtectedProductionDeployIsRefusedOnAnEdgeThatCannotGateAndCreatesNoGate(t *testing.T) {
	client, vendor := productionServed(t)
	req := productionDeployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).Gates(false)

	result, _ := deploy(t, client, protecting(req))

	if result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want it refused: the edge would serve protected production to anyone")
	}
	if got := result.GetError(); !strings.Contains(got, "protection.production") || !strings.Contains(got, string(fake.KindDirect)) {
		t.Errorf("Deploy() error = %q, want it to name protection.production and the %s edge", got, fake.KindDirect)
	}
	if _, held := recordedGateIn(t, vendor, environment.TierProduction, "shop"); held {
		t.Error("the refused deploy left a production gate recorded")
	}
}

func TestAProtectedProductionDeployIsRefusedWhereTheEdgeLeavesTheGateToAnOriginThatRunsNone(t *testing.T) {
	client, vendor := productionServed(t)
	req := productionDeployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	directEdge(vendor).Gates(true)
	directEdge(vendor).GatesAtOrigin(true)
	vendor.WithFacts(func(facts *provider.Facts) { facts.RunsGates = false })

	result, _ := deploy(t, client, protecting(req))

	if result.GetSuccess() || !strings.Contains(result.GetError(), "protection.production") {
		t.Errorf("Deploy() = %+v, want it refused naming protection.production", result)
	}
}

func TestAProtectedProductionDeployOnAnEdgeThatGatesAtTheOriginHandsTheAppTheProductionGate(t *testing.T) {
	client, vendor := productionServed(t)
	req := productionDeployRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	directEdge(vendor).Gates(true)
	directEdge(vendor).GatesAtOrigin(true)

	if result, _ := deploy(t, client, protecting(req)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	record, _ := recordedGateIn(t, vendor, environment.TierProduction, "shop")
	if got := provisionedApp(t, vendor).Gate; got == nil || !reflect.DeepEqual(*got, record.Gate) {
		t.Errorf("the app was handed %+v, want the production gate %+v", got, record.Gate)
	}
}

func TestAProductionPublicationReachesOnlyTheProductionEdgeStack(t *testing.T) {
	client, vendor := productionServed(t)
	if result, _ := deploy(t, client, protecting(productionDeployRequest())); !result.GetSuccess() {
		t.Fatal("Deploy() failed")
	}
	before := len(relayEdge(vendor).Publications())

	status := getProtectionIn(t, client, environmentv1.Tier_TIER_PRODUCTION)

	if !status.GetRecorded() || !status.GetEnabled() || len(status.GetEdges()) != 1 || status.GetEdges()[0].GetKind() != string(fake.KindRelay) {
		t.Errorf("the production status is recorded %t, enabled %t, held by %d edges, want a recorded, enabled gate held by the relay edge", status.GetRecorded(), status.GetEnabled(), len(status.GetEdges()))
	}
	if preview := getProtection(t, client); preview.GetRecorded() {
		t.Errorf("the preview status is recorded, want none: only production deployed")
	}
	if after := len(relayEdge(vendor).Publications()); after != before {
		t.Errorf("reading the status published %d times, want none", after-before)
	}
}

func TestChangingTheProductionPasswordLeavesThePreviewPasswordAlone(t *testing.T) {
	client, vendor := productionServed(t)
	seedGates(t, vendor, recordedGates)
	if result, _ := deploy(t, client, protecting(productionDeployRequest())); !result.GetSuccess() {
		t.Fatal("Deploy() failed")
	}
	rotation := rotatePassword()
	rotation.Tier = environmentv1.Tier_TIER_PRODUCTION

	changed, err := changeProtection(t, client, rotation)

	if err != nil || changed.GetPassword() == "" {
		t.Fatalf("ChangeProtection() = rotated %t, %v, want a rotated production password", changed.GetPassword() != "", err)
	}
	production, _ := recordedGateIn(t, vendor, environment.TierProduction, "shop")
	if production.Password != changed.GetPassword() {
		t.Errorf("the production record holds %q, want the rotated %q", production.Password, changed.GetPassword())
	}
	if preview, _ := recordedGateOf(t, vendor, "shop"); preview.Password != shopPassword {
		t.Errorf("the preview record holds %q, want it untouched", preview.Password)
	}
}
