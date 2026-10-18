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
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var shopGate = gate.Gate{
	Key:           "shop-gate-key",
	PasswordHash:  gate.HashPassword(shopPassword),
	BypassSecrets: []string{"shop-bypass"},
	AllowOptions:  []string{"/api/"},
}

const shopPassword = "correct horse battery"

var otherGate = gate.Gate{Key: "other-gate-key", PasswordHash: "other-password-hash", BypassSecrets: []string{}, AllowOptions: []string{}}

var recordedGates = map[string]stackrecords.GateRecord{
	"shop":  {Gate: shopGate, Password: shopPassword, Enabled: true},
	"other": {Gate: otherGate, Password: "other-password", Enabled: true},
}

func seedGates(t *testing.T, vendor *fake.Provider, records map[string]stackrecords.GateRecord) {
	t.Helper()
	seedGatesIn(t, vendor, environment.TierPreview, records)
}

func seedGatesIn(t *testing.T, vendor *fake.Provider, tier environment.Tier, gates map[string]stackrecords.GateRecord) {
	t.Helper()
	plaintext, err := json.Marshal(gates)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := vendor.Cipher().Seal(t.Context(), tier, stackrecords.GatesAssociatedData, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	value, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := records.ReadOrEmpty(t.Context(), vendor.KeyValues(), stackrecords.NewGatesRecordKey(tier))
	if err != nil {
		t.Fatal(err)
	}
	entry.Value = value
	if _, err := vendor.KeyValues().Write(t.Context(), entry); err != nil {
		t.Fatal(err)
	}
}

func recordedGateOf(t *testing.T, vendor *fake.Provider, slug string) (stackrecords.GateRecord, bool) {
	t.Helper()
	return recordedGateIn(t, vendor, environment.TierPreview, slug)
}

func recordedGateIn(t *testing.T, vendor *fake.Provider, tier environment.Tier, slug string) (stackrecords.GateRecord, bool) {
	t.Helper()
	records, err := stackrecords.ReadGates(t.Context(), vendor.KeyValues(), vendor.Cipher(), tier)
	if err != nil {
		t.Fatal(err)
	}
	record, held := records[slug]
	return record, held
}

func protecting(req *contractv1.DeployRequest) *contractv1.DeployRequest {
	req.Protect = true
	return req
}

func gatedPreviewDeployed(t *testing.T, req *contractv1.DeployRequest) *fake.Provider {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	seedGates(t, vendor, recordedGates)
	if result, _ := deploy(t, client, protecting(req)); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	return vendor
}

func TestAProtectedPreviewDeployHandsItsStackTheGateRecordedForItsProject(t *testing.T) {
	vendor := gatedPreviewDeployed(t, declaresPreview(previewDeployRequest(), "*.preview.shop.example"))

	got := onlyStack(t, vendor).Gate
	if got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("Gate = %+v, want the gate recorded for shop, %+v: the edge publishes it to where its worker reads it", got, shopGate)
	}
}

func TestAProtectedPreviewDeployHandsAnEdgeThatChecksSignedPasswordsTheGateWithItsPasswordSigned(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	seedGates(t, vendor, recordedGates)
	relayEdge(vendor).ChecksSignedPasswords(true)
	if result, _ := deploy(t, client, protecting(previewDeployRequest())); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}

	want := shopGate
	want.PasswordHash = gate.SignPassword(shopGate.Key, shopPassword)
	if got := onlyStack(t, vendor).Gate; got == nil || !reflect.DeepEqual(*got, want) {
		t.Errorf("Gate = %+v, want the recorded gate with its password signed, %+v: the edge cannot afford to derive scrypt", got, want)
	}
	if record, _ := recordedGateOf(t, vendor, "shop"); record.Gate.PasswordHash != shopGate.PasswordHash {
		t.Errorf("the recorded gate holds %q, want the scrypt hash it held, %q", record.Gate.PasswordHash, shopGate.PasswordHash)
	}
}

func TestAProtectedPreviewDeployOnTheSharedWildcardHandsItsStackTheGateToo(t *testing.T) {
	vendor := gatedPreviewDeployed(t, previewDeployRequest())

	got := onlyStack(t, vendor).Gate
	if got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("Gate = %+v, want the gate recorded for shop, %+v", got, shopGate)
	}
}

func TestAProtectedPreviewDeployNeverHandsAnEdgeThePlainPassword(t *testing.T) {
	vendor := gatedPreviewDeployed(t, previewDeployRequest())

	if shown := fmt.Sprintf("%+v", onlyStack(t, vendor)); strings.Contains(shown, shopPassword) {
		t.Errorf("the stack spec an edge receives carries the plain password: %s", shown)
	}
}

func TestUsePreviewWildcardHandsTheSharedEntryNoGate(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	seedGates(t, vendor, recordedGates)

	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	for _, spec := range vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Specs() {
		shown := fmt.Sprintf("%+v", spec)
		for slug, record := range recordedGates {
			if strings.Contains(shown, record.Gate.Key) {
				t.Errorf("the shared entry's spec carries %s's gate key, want no gate: each project's gate is published to where its worker reads it", slug)
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
	if len(record.Password) < 12 || record.Gate.Key == "" || !record.Gate.HasPassword(record.Password) {
		t.Errorf("the recorded gate is %+v, want a generated password of 12 or more characters, its hash and a random key", record)
	}
	if got := onlyStack(t, vendor).Gate; got == nil || got.Key != record.Gate.Key {
		t.Errorf("the stack received %+v, want the gate just created", got)
	}
	for _, text := range eventTexts(t, events, result) {
		if strings.Contains(text, record.Password) || strings.Contains(text, record.Gate.Key) {
			t.Errorf("the deploy said %q, and the password and key must never be shown: `ocel protection password --preview` reveals the password", text)
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
	seedGates(t, vendor, recordedGates)
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
	if got := onlyStack(t, vendor).Gate; got != nil {
		t.Errorf("Gate = %+v, want none: the edge must take its copy down", got)
	}
	if other, _ := recordedGateOf(t, vendor, "other"); !other.Enabled {
		t.Error("another project's gate was disabled with shop's")
	}
	warned := false
	for _, text := range eventTexts(t, events, result) {
		warned = warned || (strings.Contains(text, "protection.preview") && strings.Contains(text, "opens the previews of shop"))
	}
	if !warned {
		t.Error("the deploy opened shop's previews without saying so and naming protection.preview")
	}
}

func TestAProtectedPreviewDeployGatesWhatItCanAndSaysWhichOtherPreviewsIgnoreTheGate(t *testing.T) {
	client, vendor, req := originGatedPreviewServed(t)
	previewIgnoringTheGate(t, vendor, "pr-3")

	result, events := deploy(t, client, protecting(req))

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed: refusing would leave every preview open, the ones that check the gate too", result.GetError())
	}
	if got := provisionedApp(t, vendor).Gate; got == nil {
		t.Error("the deploy handed its app no gate")
	}
	warned := false
	for _, text := range eventTexts(t, events, result) {
		warned = warned || (strings.Contains(text, "api") && strings.Contains(text, "pr-3") && strings.Contains(text, "answer"))
	}
	if !warned {
		t.Errorf("the deploy protected shop without saying function api of preview pr-3 still answers anyone: %q", eventTexts(t, events, result))
	}
	if note := noteOf(result); !strings.Contains(note, "pr-3") {
		t.Errorf("the result's notes = %q, want them to name preview pr-3", note)
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
	if got := stacks[len(stacks)-1].Gate; got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("the edge was handed %+v, want the kept gate", got)
	}
}

func TestAnUnprotectedPreviewDeployWhoseEdgeWriteFailsKeepsThePreviewsProtected(t *testing.T) {
	client, vendor := unprotectedPreviewServed(t)
	relayEdge(vendor).OnReconcile(func(edge.StackSpec) { relayEdge(vendor).Refuse(errors.New("the edge is unreachable")) })

	if result, _ := deploy(t, client, previewDeployRequest()); result.GetSuccess() {
		t.Fatal("Deploy() succeeded, want the edge failure")
	}

	if record, _ := recordedGateOf(t, vendor, "shop"); !record.Enabled || !record.Pending || record.Password != shopPassword {
		t.Errorf("shop's record is %+v, want it enabled again with its password and pending: the edge may hold the gate or may have taken it down before failing", record)
	}
}

func TestACredentialChangeDuringADeploysEdgeWriteIsWhatTheEdgeHoldsAfterward(t *testing.T) {
	client, vendor := protectedPreview(t)
	var rotated *contractv1.ChangeProtectionResponse
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
	if got := stacks[len(stacks)-1].Gate; got == nil || got.Key == record.Gate.Key {
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
	seedGates(t, vendor, recordedGates)
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
	seedGates(t, vendor, recordedGates)
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

	got := onlyDirectStack(t, vendor).Gate
	if got == nil || !reflect.DeepEqual(*got, shopGate) {
		t.Errorf("Gate = %+v, want the gate recorded for shop, %+v", got, shopGate)
	}
}

func directPreviewServed(t *testing.T, records map[string]stackrecords.GateRecord) (contractv1connect.ProviderServiceClient, *fake.Provider, *contractv1.DeployRequest) {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	vendor.Edges().(*fake.Edges).Edge(fake.KindDirect).Gates(false)
	seedGates(t, vendor, records)
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
	if got := result.GetError(); !strings.Contains(got, "protection.preview") || !strings.Contains(got, string(fake.KindDirect)) {
		t.Errorf("Deploy() error = %q, want it to name protection.preview and the %s edge", got, fake.KindDirect)
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
