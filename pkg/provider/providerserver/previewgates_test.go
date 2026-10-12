package providerserver_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
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
	PasswordMAC:   previewgate.HashPassword("shop-gate-key", shopPassword),
	BypassSecrets: []string{"shop-bypass"},
	AllowOptions:  []string{"/api/"},
}

const shopPassword = "correct horse battery"

var otherGate = previewgate.Gate{Key: "other-gate-key", PasswordMAC: "other-password-mac", BypassSecrets: []string{}, AllowOptions: []string{}}

var recordedGates = map[string]stackrecords.PreviewGateRecord{
	"shop":  {Gate: shopGate, Password: shopPassword, Enabled: true},
	"other": {Gate: otherGate, Password: "other-password", Enabled: true},
}

func seedPreviewGates(t *testing.T, vendor *fake.Provider, records map[string]stackrecords.PreviewGateRecord) {
	t.Helper()
	plaintext, err := json.Marshal(records)
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

func recordedGateOf(t *testing.T, vendor *fake.Provider, slug string) (stackrecords.PreviewGateRecord, bool) {
	t.Helper()
	records, err := stackrecords.ReadPreviewGates(t.Context(), vendor.KeyValues(), vendor.Cipher())
	if err != nil {
		t.Fatal(err)
	}
	record, held := records[slug]
	return record, held
}

func protecting(req *contractv1.DeployRequest) *contractv1.DeployRequest {
	req.ProtectPreviews = true
	return req
}

func gatedPreviewDeployed(t *testing.T, req *contractv1.DeployRequest) *fake.Provider {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	seedPreviewGates(t, vendor, recordedGates)
	if result, _ := deploy(t, client, protecting(req)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	return vendor
}

func TestAProtectedPreviewDeployHandsItsStackTheGateRecordedForItsProject(t *testing.T) {
	vendor := gatedPreviewDeployed(t, declaresPreview(previewDeployRequest(), "*.preview.shop.example"))

	got := onlyStack(t, vendor).PreviewGate
	if got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("PreviewGate = %+v, want the gate recorded for shop, %+v: the edge publishes it to where its worker reads it", got, shopGate)
	}
}

func TestAProtectedPreviewDeployOnTheSharedWildcardHandsItsStackTheGateToo(t *testing.T) {
	vendor := gatedPreviewDeployed(t, previewDeployRequest())

	got := onlyStack(t, vendor).PreviewGate
	if got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("PreviewGate = %+v, want the gate recorded for shop, %+v", got, shopGate)
	}
}

func TestAProtectedPreviewDeployNeverHandsAnEdgeThePlainPassword(t *testing.T) {
	vendor := gatedPreviewDeployed(t, previewDeployRequest())

	if shown := fmt.Sprintf("%+v", onlyStack(t, vendor)); strings.Contains(shown, shopPassword) {
		t.Errorf("the stack spec an edge receives carries the plain password: %s", shown)
	}
}

func TestAProductionDeployHandsItsStackNoGate(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	seedPreviewGates(t, vendor, recordedGates)

	req := protecting(deployRequest())
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	for _, stack := range vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks() {
		if stack.PreviewGate != nil {
			t.Errorf("PreviewGate = %+v, want none: production serves no preview", stack.PreviewGate)
		}
	}
	if _, held := recordedGateOf(t, vendor, "shop"); !held {
		t.Error("a production deploy removed the project's recorded gate, and only a preview deploy settles it")
	}
}

func TestUsePreviewWildcardHandsTheSharedEntryNoGate(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	seedPreviewGates(t, vendor, recordedGates)

	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	for _, spec := range vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Specs() {
		for name := range spec.Program.Worker.Secrets {
			if strings.Contains(strings.ToUpper(name), "GATE") {
				t.Errorf("the shared entry carries Secrets[%s], want no gate: each project's gate is published to where its worker reads it", name)
			}
		}
	}
}

func TestTheFirstProtectedPreviewDeployCreatesTheGateAndShowsNoPassword(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})

	result, events := deploy(t, client, protecting(previewDeployRequest()))

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	record, held := recordedGateOf(t, vendor, "shop")
	if !held {
		t.Fatal("no gate is recorded for shop after a protected preview deploy")
	}
	if len(record.Password) < 12 || record.Gate.Key == "" || record.Gate.PasswordMAC != previewgate.HashPassword(record.Gate.Key, record.Password) {
		t.Errorf("the recorded gate is %+v, want a generated password of 12 or more characters and its MAC under a random key", record)
	}
	if got := onlyStack(t, vendor).PreviewGate; got == nil || got.Key != record.Gate.Key {
		t.Errorf("the stack received %+v, want the gate just created", got)
	}
	for _, text := range eventTexts(t, events, result) {
		if strings.Contains(text, record.Password) || strings.Contains(text, record.Gate.Key) {
			t.Errorf("the deploy said %q, and the password and key must never be shown: `ocel preview protection password` reveals the password", text)
		}
	}
}

func eventTexts(t *testing.T, events []*progressv1.OperationEvent, result *progressv1.OperationResult) []string {
	t.Helper()
	var texts []string
	for _, event := range events {
		encoded, err := protojson.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		texts = append(texts, string(encoded))
	}
	encoded, err := protojson.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return append(texts, string(encoded))
}

func TestALaterProtectedPreviewDeployKeepsTheGate(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	if result, _ := deploy(t, client, protecting(previewDeployRequest())); !result.GetSuccess() {
		t.Fatalf("first Deploy() = %q", result.GetError())
	}
	first, _ := recordedGateOf(t, vendor, "shop")

	if result, _ := deploy(t, client, protecting(previewDeployRequest())); !result.GetSuccess() {
		t.Fatalf("second Deploy() = %q", result.GetError())
	}

	if second, _ := recordedGateOf(t, vendor, "shop"); second.Gate.Key != first.Gate.Key || second.Password != first.Password {
		t.Errorf("the gate changed between deploys: %+v then %+v, want it kept", first, second)
	}
}

func unprotectedPreviewServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	seedPreviewGates(t, vendor, recordedGates)
	return client, vendor
}

func TestAPreviewDeployThatDoesNotProtectOpensThePreviewsKeepsTheCredentialsAndSaysSo(t *testing.T) {
	client, vendor := unprotectedPreviewServed(t)

	result, events := deploy(t, client, previewDeployRequest())

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	record, held := recordedGateOf(t, vendor, "shop")
	if !held || record.Enabled || record.Password != shopPassword || record.Gate.Key != shopGate.Key {
		t.Errorf("shop's record is %+v, held %t, want its credentials kept and the record disabled", record, held)
	}
	if got := onlyStack(t, vendor).PreviewGate; got != nil {
		t.Errorf("PreviewGate = %+v, want none: the edge must take its copy down", got)
	}
	if other, _ := recordedGateOf(t, vendor, "other"); !other.Enabled {
		t.Error("another project's gate was disabled with shop's")
	}
	warned := false
	for _, text := range eventTexts(t, events, result) {
		warned = warned || (strings.Contains(text, "previews.protection") && strings.Contains(text, "opens its previews"))
	}
	if !warned {
		t.Error("the deploy opened shop's previews without saying so and naming previews.protection")
	}
}

func TestTurningProtectionBackOnRestoresTheSameCredentials(t *testing.T) {
	client, vendor := unprotectedPreviewServed(t)
	if result, _ := deploy(t, client, previewDeployRequest()); !result.GetSuccess() {
		t.Fatalf("unprotected Deploy() = %q", result.GetError())
	}

	if result, _ := deploy(t, client, protecting(previewDeployRequest())); !result.GetSuccess() {
		t.Fatalf("protected Deploy() = %q", result.GetError())
	}

	record, _ := recordedGateOf(t, vendor, "shop")
	if !record.Enabled || record.Pending || record.Password != shopPassword || record.Gate.Key != shopGate.Key {
		t.Errorf("shop's record is %+v, want it enabled again with the same password and key", record)
	}
	stacks := relayEdge(vendor).Stacks()
	if got := stacks[len(stacks)-1].PreviewGate; got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("the edge was handed %+v, want the kept gate", got)
	}
}

func TestAnUnprotectedPreviewDeployWhoseEdgeWriteFailsKeepsThePreviewsProtected(t *testing.T) {
	client, vendor := unprotectedPreviewServed(t)
	relayEdge(vendor).OnReconcile(func(edge.StackSpec) { relayEdge(vendor).Refuse(errors.New("the edge is unreachable")) })

	if result, _ := deploy(t, client, previewDeployRequest()); result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want the edge failure")
	}

	if record, _ := recordedGateOf(t, vendor, "shop"); !record.Enabled || record.Password != shopPassword {
		t.Errorf("shop's record is %+v, want it still enabled with its password: the edge still holds the gate", record)
	}
}

func TestACredentialChangeDuringADeploysEdgeWriteIsWhatTheEdgeHoldsAfterward(t *testing.T) {
	client, vendor := protectedPreview(t)
	var rotated *contractv1.ChangePreviewProtectionResponse
	relayEdge(vendor).OnReconcile(func(edge.StackSpec) {
		relayEdge(vendor).OnReconcile(nil)
		rotated, _ = changeAndPublish(t, client, rotatePassword())
	})

	if result, _ := deploy(t, client, protecting(previewDeployRequest())); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	record, _ := recordedGateOf(t, vendor, "shop")
	if rotated.GetPassword() == "" || record.Password != rotated.GetPassword() {
		t.Fatalf("the record holds password %q, want the one rotated mid-deploy", record.Password)
	}
	stacks := relayEdge(vendor).Stacks()
	if got := stacks[len(stacks)-1].PreviewGate; got == nil || got.Key == record.Gate.Key {
		t.Fatalf("the deploy wrote %+v, want it to have written the gate it read before the rotation", got)
	}
	republished := 0
	for _, published := range relayEdge(vendor).Publications() {
		if published.Gate != nil && published.Gate.Key == record.Gate.Key {
			republished++
		}
	}
	if republished < 2 {
		t.Errorf("the rotated gate was published %d times, want the deploy to publish it again after writing the older gate over it", republished)
	}
	if record.Pending {
		t.Error("the record is pending although the edge holds it")
	}
}

func TestADryProtectedPreviewDeployRecordsNothing(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	req := protecting(previewDeployRequest())
	req.Dry = true

	deploy(t, client, req)

	if _, held := recordedGateOf(t, vendor, "shop"); held {
		t.Error("a dry deploy recorded a gate, and a plan must change nothing")
	}
}

func TestADryUnprotectedPreviewDeployDisablesNothing(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	seedPreviewGates(t, vendor, recordedGates)
	req := previewDeployRequest()
	req.Dry = true

	deploy(t, client, req)

	if record, _ := recordedGateOf(t, vendor, "shop"); !record.Enabled {
		t.Error("a dry deploy disabled the gate, and a plan must change nothing")
	}
}

func TestADeployThatReachedTheEdgeMarksTheGatePublished(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})

	if result, _ := deploy(t, client, protecting(previewDeployRequest())); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	if record, _ := recordedGateOf(t, vendor, "shop"); record.Pending {
		t.Error("the gate is still pending after a deploy wrote it to the edge")
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
	req := protecting(declaresPreview(previewDeployRequest(), hostnames...))
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

func TestAProtectedPreviewDeployOnAnEdgeThatRunsNoCodeHandsItsStackTheGate(t *testing.T) {
	vendor := gatedDirectPreviewDeployed(t, "", "*.preview.shop.example")

	got := onlyDirectStack(t, vendor).PreviewGate
	if got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("PreviewGate = %+v, want the gate recorded for shop, %+v", got, shopGate)
	}
}

func directPreviewServed(t *testing.T, records map[string]stackrecords.PreviewGateRecord) (contractv1connect.ProviderServiceClient, *fake.Provider, *contractv1.DeployRequest) {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).GatesPreviews(false)
	seedPreviewGates(t, vendor, records)
	req := declaresPreview(previewDeployRequest(), "*.preview.shop.example")
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}
	return client, vendor, req
}

func TestAProtectedPreviewDeployIsRefusedOnAnEdgeThatCannotGateAndCreatesNoGate(t *testing.T) {
	client, vendor, req := directPreviewServed(t, nil)

	result, _ := deploy(t, client, protecting(req))

	if result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want it refused: the edge would serve the protected preview to anyone")
	}
	if got := result.GetError(); !strings.Contains(got, "previews.protection") || !strings.Contains(got, string(fake.KindDirect)) {
		t.Errorf("Deploy() error = %q, want it to name previews.protection and the %s edge", got, fake.KindDirect)
	}
	if _, held := recordedGateOf(t, vendor, "shop"); held {
		t.Error("the refused deploy left a gate recorded")
	}
	if stacks := vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).Stacks(); len(stacks) != 0 {
		t.Errorf("the edge reconciled %d stacks, want none: the refusal comes before anything serves the preview", len(stacks))
	}
}

func TestAnUnprotectedPreviewDeployGoesAheadOnAnEdgeThatCannotGate(t *testing.T) {
	client, _, req := directPreviewServed(t, nil)

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed: nothing asks for protection", result.GetError())
	}
}
