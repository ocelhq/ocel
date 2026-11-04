package providerserver_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func protectedPreview(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	if result, _ := deploy(t, client, protecting(previewDeployRequest())); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	return client, vendor
}

func relayEdge(vendor *fake.Provider) *fake.Edge {
	return vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
}

func lastPublication(t *testing.T, vendor *fake.Provider) edge.GatePublication {
	t.Helper()
	published := relayEdge(vendor).Publications()
	if len(published) == 0 {
		t.Fatal("the edge was published nothing")
	}
	return published[len(published)-1]
}

func changeProtection(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ChangeProtectionRequest) (*contractv1.ChangeProtectionResponse, error) {
	t.Helper()
	if req.Tier == environmentv1.Tier_TIER_UNSPECIFIED {
		req.Tier = environmentv1.Tier_TIER_PREVIEW
	}
	if req.Slug == "" {
		req.Slug = "shop"
	}
	if req.Edge == nil {
		req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	}
	return client.ChangeProtection(context.Background(), req)
}

func publishProtection(t *testing.T, client contractv1connect.ProviderServiceClient) (*progressv1.OperationResult, []*progressv1.OperationEvent, error) {
	t.Helper()
	return publishProtectionThrough(t, client, fake.KindRelay)
}

func publishProtectionThrough(t *testing.T, client contractv1connect.ProviderServiceClient, kind edge.Kind) (*progressv1.OperationResult, []*progressv1.OperationEvent, error) {
	t.Helper()
	stream, err := client.PublishProtection(context.Background(), &contractv1.PublishProtectionRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
		Slug: "shop",
		Edge: &contractv1.EdgeSelection{Kind: string(kind)},
	})
	if err != nil {
		t.Fatalf("PublishProtection() error = %v", err)
	}
	defer stream.Close()
	var events []*progressv1.OperationEvent
	var result *progressv1.OperationResult
	for stream.Receive() {
		events = append(events, stream.Msg())
		if got := stream.Msg().GetResult(); got != nil {
			result = got
		}
	}
	return result, events, stream.Err()
}

func changeAndPublish(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ChangeProtectionRequest) (*contractv1.ChangeProtectionResponse, *progressv1.OperationResult) {
	t.Helper()
	changed, err := changeProtection(t, client, req)
	if err != nil {
		t.Fatalf("ChangeProtection() = %v", err)
	}
	result, _, err := publishProtection(t, client)
	if err != nil {
		t.Fatalf("PublishProtection() = %v", err)
	}
	return changed, result
}

func rotatePassword() *contractv1.ChangeProtectionRequest {
	return &contractv1.ChangeProtectionRequest{Change: &contractv1.ChangeProtectionRequest_RotatePassword{RotatePassword: &contractv1.RotatePassword{}}}
}

func setPassword(password string) *contractv1.ChangeProtectionRequest {
	return &contractv1.ChangeProtectionRequest{Change: &contractv1.ChangeProtectionRequest_SetPassword{SetPassword: &contractv1.SetPassword{Password: password}}}
}

func createBypass() *contractv1.ChangeProtectionRequest {
	return &contractv1.ChangeProtectionRequest{Change: &contractv1.ChangeProtectionRequest_CreateBypassSecret{CreateBypassSecret: &contractv1.CreateBypassSecret{}}}
}

func revokeBypass(id string) *contractv1.ChangeProtectionRequest {
	return &contractv1.ChangeProtectionRequest{Change: &contractv1.ChangeProtectionRequest_RevokeBypassSecret{RevokeBypassSecret: &contractv1.RevokeBypassSecret{Id: id}}}
}

func getProtection(t *testing.T, client contractv1connect.ProviderServiceClient) *contractv1.GetProtectionResponse {
	t.Helper()
	return getProtectionIn(t, client, environmentv1.Tier_TIER_PREVIEW)
}

func getProtectionIn(t *testing.T, client contractv1connect.ProviderServiceClient, tier environmentv1.Tier) *contractv1.GetProtectionResponse {
	t.Helper()
	resp, err := client.GetProtection(context.Background(), &contractv1.GetProtectionRequest{Tier: tier, Slug: "shop", Edge: &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}})
	if err != nil {
		t.Fatalf("GetProtection() = %v", err)
	}
	return resp
}

func getProtectionThrough(t *testing.T, client contractv1connect.ProviderServiceClient, kind edge.Kind) *contractv1.GetProtectionResponse {
	t.Helper()
	resp, err := client.GetProtection(context.Background(), &contractv1.GetProtectionRequest{Tier: environmentv1.Tier_TIER_PREVIEW, Slug: "shop", Edge: &contractv1.EdgeSelection{Kind: string(kind)}})
	if err != nil {
		t.Fatalf("GetProtection() = %v", err)
	}
	return resp
}

func revealProtectionPassword(t *testing.T, client contractv1connect.ProviderServiceClient) *contractv1.RevealProtectionPasswordResponse {
	t.Helper()
	resp, err := client.RevealProtectionPassword(context.Background(), &contractv1.RevealProtectionPasswordRequest{Tier: environmentv1.Tier_TIER_PREVIEW, Slug: "shop"})
	if err != nil {
		t.Fatalf("RevealProtectionPassword() = %v", err)
	}
	return resp
}

func TestAProjectWithNoRecordedGateReportsNoneAndRevealsNothing(t *testing.T) {
	client, _ := contractServed(t, "1.0.0")

	resp := getProtection(t, client)
	revealed := revealProtectionPassword(t, client)

	if resp.GetRecorded() || len(resp.GetBypassSecretIds()) != 0 || len(resp.GetEdges()) != 0 {
		t.Errorf("GetProtection() = recorded %t, bypass ids %v, edges %v, want an empty answer: nothing is recorded for shop",
			resp.GetRecorded(), resp.GetBypassSecretIds(), resp.GetEdges())
	}
	if revealed.GetRecorded() || revealed.GetPassword() != "" {
		t.Errorf("RevealProtectionPassword() = recorded %t, password revealed %t, want nothing: no password is recorded for shop",
			revealed.GetRecorded(), revealed.GetPassword() != "")
	}
}

func TestTheStatusOfAProtectedPreviewNamesItsEdgeAndNeverCarriesThePassword(t *testing.T) {
	client, vendor := protectedPreview(t)
	record, _ := recordedGateOf(t, vendor, "shop")

	status := getProtection(t, client)

	wire, err := proto.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if !status.GetRecorded() || strings.Contains(string(wire), record.Password) {
		t.Errorf("status = recorded %t, password on the wire %t, want the gate recorded and the password left out",
			status.GetRecorded(), strings.Contains(string(wire), record.Password))
	}
	if edges := status.GetEdges(); len(edges) != 1 || edges[0].GetKind() != string(fake.KindRelay) || !edges[0].GetPublished() {
		t.Errorf("edges = %v, want the relay edge, live: the deploy that created the gate wrote it there", edges)
	}
}

func TestRevealingTheProtectionPasswordReturnsTheRecordedOne(t *testing.T) {
	client, vendor := protectedPreview(t)
	record, _ := recordedGateOf(t, vendor, "shop")

	revealed := revealProtectionPassword(t, client)

	if !revealed.GetRecorded() || revealed.GetPassword() != record.Password {
		t.Errorf("RevealProtectionPassword() = recorded %t, the recorded password %t, want it recorded and revealed",
			revealed.GetRecorded(), revealed.GetPassword() == record.Password)
	}
}

func TestRotatingThePasswordRecordsAndPublishesAGateTheOldPasswordAndSessionsFailAgainst(t *testing.T) {
	client, vendor := protectedPreview(t)
	before, _ := recordedGateOf(t, vendor, "shop")

	changed, result := changeAndPublish(t, client, rotatePassword())

	if !result.GetSuccess() {
		t.Fatalf("PublishProtection() = %v, want it to succeed", result)
	}
	after, _ := recordedGateOf(t, vendor, "shop")
	generated := changed.GetPassword()
	if generated == "" || generated == before.Password || after.Password != generated {
		t.Errorf("the change answered password %q, the record %q, was %q, want a new one in both", generated, after.Password, before.Password)
	}
	if after.Gate.Key == before.Gate.Key || !after.Gate.HasPassword(generated) {
		t.Errorf("the gate is %+v after the rotation, want a new key beside the new password's hash, so every session ends", after.Gate)
	}
	if published := lastPublication(t, vendor); published.Gate == nil || published.Gate.Key != after.Gate.Key {
		t.Errorf("the edge was published %+v, want the rotated gate", published.Gate)
	}
	if after.Pending {
		t.Error("the gate is still pending after every edge took it")
	}
}

func TestThePublicationStreamCarriesNoSecretTheChangeGenerated(t *testing.T) {
	client, _ := protectedPreview(t)
	rotated, err := changeProtection(t, client, rotatePassword())
	if err != nil {
		t.Fatal(err)
	}
	created, err := changeProtection(t, client, createBypass())
	if err != nil {
		t.Fatal(err)
	}

	result, events, err := publishProtection(t, client)

	if err != nil || !result.GetSuccess() {
		t.Fatalf("PublishProtection() = %v, %v", result, err)
	}
	for _, text := range eventTexts(t, events, result) {
		for _, secret := range []string{rotated.GetPassword(), created.GetBypassSecret()} {
			if strings.Contains(text, secret) {
				t.Errorf("the publication stream carried a generated secret: %s", text)
			}
		}
	}
}

func TestSettingThePasswordKeepsTheOneTheCallerChoseAndRotatesTheKey(t *testing.T) {
	client, vendor := protectedPreview(t)
	before, _ := recordedGateOf(t, vendor, "shop")

	changed, result := changeAndPublish(t, client, setPassword("a password I picked"))

	if !result.GetSuccess() {
		t.Fatalf("PublishProtection() = %v", result)
	}
	after, _ := recordedGateOf(t, vendor, "shop")
	if after.Password != "a password I picked" || after.Gate.Key == before.Gate.Key {
		t.Errorf("the record holds %+v, want the chosen password under a new key", after)
	}
	if changed.GetPassword() != "" {
		t.Error("the change echoes the password the caller chose")
	}
}

func TestAPasswordShorterThanTwelveCharactersIsRefusedWithoutChangingAnything(t *testing.T) {
	client, vendor := protectedPreview(t)
	before, _ := recordedGateOf(t, vendor, "shop")

	_, err := changeProtection(t, client, setPassword("too short"))

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("ChangeProtection() = %v, want an invalid-argument refusal", err)
	}
	if after, _ := recordedGateOf(t, vendor, "shop"); after.Gate.Key != before.Gate.Key {
		t.Error("a refused password rotated the key")
	}
}

func TestCreatingABypassSecretAddsItWithoutRotatingTheKey(t *testing.T) {
	client, vendor := protectedPreview(t)
	before, _ := recordedGateOf(t, vendor, "shop")

	changed, result := changeAndPublish(t, client, createBypass())

	if !result.GetSuccess() {
		t.Fatalf("PublishProtection() = %v", result)
	}
	secret, id := changed.GetBypassSecret(), changed.GetBypassSecretId()
	after, _ := recordedGateOf(t, vendor, "shop")
	if secret == "" || len(id) != 12 || len(after.Gate.BypassSecrets) != 1 || after.Gate.BypassSecrets[0] != secret {
		t.Errorf("secret %q id %q record %+v, want one secret recorded under the id returned", secret, id, after.Gate)
	}
	if after.Gate.Key != before.Gate.Key {
		t.Error("creating a bypass secret rotated the key, and that ends every session for nothing")
	}
	if listed := getProtection(t, client).GetBypassSecretIds(); len(listed) != 1 || listed[0] != id {
		t.Errorf("status lists %v, want %q", listed, id)
	}
}

func TestAProjectHoldsAtMostEightBypassSecrets(t *testing.T) {
	client, vendor := protectedPreview(t)
	for range fakeMaxBypassSecrets {
		if _, err := changeProtection(t, client, createBypass()); err != nil {
			t.Fatalf("creating a bypass secret = %v", err)
		}
	}

	_, err := changeProtection(t, client, createBypass())

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("the ninth secret = %v, want it refused", err)
	}
	if after, _ := recordedGateOf(t, vendor, "shop"); len(after.Gate.BypassSecrets) != fakeMaxBypassSecrets {
		t.Errorf("the record holds %d secrets, want %d", len(after.Gate.BypassSecrets), fakeMaxBypassSecrets)
	}
}

const fakeMaxBypassSecrets = 8

func TestRevokingABypassSecretRemovesItAndRotatesTheKeyKeepingThePasswordWorking(t *testing.T) {
	client, vendor := protectedPreview(t)
	created, _ := changeAndPublish(t, client, createBypass())
	before, _ := recordedGateOf(t, vendor, "shop")

	_, result := changeAndPublish(t, client, revokeBypass(created.GetBypassSecretId()))

	if !result.GetSuccess() {
		t.Fatalf("PublishProtection() = %v", result)
	}
	after, _ := recordedGateOf(t, vendor, "shop")
	if len(after.Gate.BypassSecrets) != 0 || after.Gate.Key == before.Gate.Key {
		t.Errorf("the gate is %+v, want the secret gone and a new key so the sessions it opened end", after.Gate)
	}
	if !after.Gate.HasPassword(after.Password) {
		t.Error("the password no longer matches the gate after the key rotated")
	}
}

func TestRevokingAnUnknownBypassSecretIsRefused(t *testing.T) {
	client, _ := protectedPreview(t)

	_, err := changeProtection(t, client, revokeBypass("0123456789ab"))

	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "ocel protection bypass ls --preview") {
		t.Errorf("ChangeProtection() = %v, want a refusal that points at `ocel protection bypass ls --preview`", err)
	}
}

func TestSyncingPublishesTheRecordedGateAndClearsWhatWasPending(t *testing.T) {
	client, vendor := protectedPreview(t)
	relayEdge(vendor).RefusePublication(errors.New("the edge is unreachable"))
	if _, failed := changeAndPublish(t, client, createBypass()); failed.GetSuccess() {
		t.Fatal("a publication the edge refused still succeeded")
	}
	relayEdge(vendor).RefusePublication(nil)

	result, _, err := publishProtection(t, client)

	if err != nil || !result.GetSuccess() {
		t.Fatalf("sync = %v, %v, want it to succeed", result, err)
	}
	record, _ := recordedGateOf(t, vendor, "shop")
	if record.Pending {
		t.Error("the gate is still pending after a sync every edge took")
	}
	if published := lastPublication(t, vendor); published.Gate == nil || len(published.Gate.BypassSecrets) != 1 {
		t.Errorf("the sync published %+v, want the recorded gate with the secret created while the edge was down", published.Gate)
	}
}

func TestSyncingToAnEdgeThatChecksSignedPasswordsPublishesTheRecordedGateWithItsPasswordSigned(t *testing.T) {
	client, vendor := protectedPreview(t)
	relayEdge(vendor).ChecksSignedPasswords(true)

	if result, _, err := publishProtection(t, client); err != nil || !result.GetSuccess() {
		t.Fatalf("sync = %v, %v, want it to succeed", result, err)
	}

	record, _ := recordedGateOf(t, vendor, "shop")
	want := record.Gate
	want.PasswordHash = gate.SignPassword(record.Gate.Key, record.Password)
	if published := lastPublication(t, vendor); published.Gate == nil || !reflect.DeepEqual(*published.Gate, want) {
		t.Fatalf("the sync published %+v, want the recorded gate with its password signed, %+v", published.Gate, want)
	}
	if signed := lastPublication(t, vendor).Gate; !signed.HasPassword(record.Password) {
		t.Error("the published gate refuses the recorded password")
	}
}

func TestASyncThatTheEdgeRefusesLeavesTheGatePending(t *testing.T) {
	client, vendor := protectedPreview(t)
	relayEdge(vendor).RefusePublication(errors.New("the edge is unreachable"))

	result, _, err := publishProtection(t, client)

	if err != nil || result.GetSuccess() {
		t.Fatalf("sync = %v, %v, want the failure in the result", result, err)
	}
	if record, _ := recordedGateOf(t, vendor, "shop"); !record.Pending {
		t.Error("the record is not pending after a sync the edge refused, so `status` would call it live")
	}
}

func TestAnEdgeThatRefusesThePublicationFailsItAndLeavesTheChangeRecordedAndPending(t *testing.T) {
	client, vendor := protectedPreview(t)
	relayEdge(vendor).RefusePublication(errors.New("the edge is unreachable"))

	changed, result := changeAndPublish(t, client, rotatePassword())

	if result.GetSuccess() || !strings.Contains(result.GetError(), "ocel protection sync --preview") {
		t.Errorf("result = %+v, want a failure that names `ocel protection sync --preview`, which publishes the preview tier's credentials", result)
	}
	if record, _ := recordedGateOf(t, vendor, "shop"); record.Password != changed.GetPassword() || !record.Pending {
		t.Errorf("the record is %+v, want the new password recorded and pending", record)
	}
	if edges := result.GetProtection().GetEdges(); len(edges) != 1 || edges[0].GetPublished() || !strings.Contains(edges[0].GetError(), "unreachable") {
		t.Errorf("edges = %v, want the relay edge, not published, with the reason", edges)
	}
	if edges := getProtection(t, client).GetEdges(); len(edges) != 1 || edges[0].GetPublished() {
		t.Errorf("status edges = %v, want the relay edge pending", edges)
	}
}

func TestAChangeBeforeAnyPreviewHasDeployedRecordsTheGateAndHasNoEdgeToPublishTo(t *testing.T) {
	client, vendor := contractServed(t, "1.0.0")

	_, result := changeAndPublish(t, client, createBypass())

	if !result.GetSuccess() {
		t.Fatalf("PublishProtection() = %v, want it to succeed: the first preview deploy publishes what is recorded", result)
	}
	record, held := recordedGateOf(t, vendor, "shop")
	if !held || len(record.Password) < 12 || len(record.Gate.BypassSecrets) != 1 {
		t.Errorf("the record is %+v, held %t, want a gate with a generated password and the new secret", record, held)
	}
	if len(result.GetProtection().GetEdges()) != 0 {
		t.Errorf("edges = %v, want none: no preview has been deployed", result.GetProtection().GetEdges())
	}
}

func TestSyncingAProjectWithNoRecordedGateSaysThereIsNothingToPublishAndWritesNoRecord(t *testing.T) {
	client, vendor := contractServed(t, "1.0.0")

	result, _, err := publishProtection(t, client)

	if err != nil || !result.GetSuccess() {
		t.Fatalf("sync = %v, %v, want it to succeed", result, err)
	}
	if _, held := recordedGateOf(t, vendor, "shop"); held {
		t.Error("a sync created a gate")
	}
}

func TestAChangeNamingAnotherEdgeThanTheOneServingTheProjectIsRefused(t *testing.T) {
	client, _ := protectedPreview(t)

	_, err := changeProtection(t, client, &contractv1.ChangeProtectionRequest{
		Edge:   &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Change: rotatePassword().Change,
	})

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("ChangeProtection() = %v, want a refusal: the project's previews are served through the relay edge", err)
	}
}

func TestAChangeWhileTheLastDeployOpenedThePreviewsIsRefusedAndKeepsTheCredentials(t *testing.T) {
	client, vendor := protectedPreview(t)
	before, _ := recordedGateOf(t, vendor, "shop")
	if result, _ := deploy(t, client, previewDeployRequest()); !result.GetSuccess() {
		t.Fatalf("unprotected Deploy() = %q", result.GetError())
	}

	_, err := changeProtection(t, client, rotatePassword())

	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "protection.preview") {
		t.Errorf("ChangeProtection() = %v, want a refusal naming protection.preview", err)
	}
	if after, _ := recordedGateOf(t, vendor, "shop"); after.Password != before.Password || after.Gate.Key != before.Gate.Key {
		t.Error("a refused change altered the kept credentials")
	}
}

func TestASyncWhileTheLastDeployOpenedThePreviewsIsRefusedAndPublishesNothing(t *testing.T) {
	client, vendor := protectedPreview(t)
	if result, _ := deploy(t, client, previewDeployRequest()); !result.GetSuccess() {
		t.Fatalf("unprotected Deploy() = %q", result.GetError())
	}
	published := len(relayEdge(vendor).Publications())

	result, _, err := publishProtection(t, client)

	if err == nil && result.GetSuccess() {
		t.Fatalf("sync = %v, want a refusal: the previews are open and nothing it could publish would protect them", result)
	}
	if said := err; said == nil || !strings.Contains(said.Error(), "protection.preview") {
		t.Errorf("sync error = %v, want it to name protection.preview and the deploy that restores protection", err)
	}
	if got := len(relayEdge(vendor).Publications()); got != published {
		t.Errorf("the edge was published %d gates by a refused sync, want none", got-published)
	}
}

func TestPublishingThroughAnEdgeThatRunsNoGateFailsSayingThePreviewsAnswerAnyone(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.0.0")
	previewBootstrapped(t, client)
	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
	relayEdge(vendor).Gates(false)
	if result, _ := deploy(t, client, previewDeployRequest()); !result.GetSuccess() {
		t.Fatalf("unprotected Deploy() = %q", result.GetError())
	}
	if _, err := changeProtection(t, client, createBypass()); err != nil {
		t.Fatal(err)
	}

	result, _, err := publishProtection(t, client)

	if err != nil || result.GetSuccess() {
		t.Fatalf("PublishProtection() = %v, %v, want the failure in the result", result, err)
	}
	if edges := result.GetProtection().GetEdges(); len(edges) != 1 || !strings.Contains(edges[0].GetError(), "runs no gate") {
		t.Errorf("edges = %v, want the relay edge, saying it runs no gate rather than that no preview deployed", edges)
	}
	if edges := getProtection(t, client).GetEdges(); len(edges) != 1 || edges[0].GetPublished() || !strings.Contains(edges[0].GetError(), "runs no gate") {
		t.Errorf("status edges = %v, want the relay edge, saying it runs no gate", edges)
	}
}

func TestEveryResponseCarryingThePreviewPasswordMarksItDebugRedactSoRedactvetRefusesToPrintTheResponse(t *testing.T) {
	for _, message := range []proto.Message{&contractv1.RevealProtectionPasswordResponse{}, &contractv1.ChangeProtectionResponse{}} {
		descriptor := message.ProtoReflect().Descriptor()
		field := descriptor.Fields().ByName("password")
		if options, _ := field.Options().(*descriptorpb.FieldOptions); !options.GetDebugRedact() {
			t.Errorf("%s.password is not marked debug_redact, want it marked so redactvet refuses to print the response", descriptor.Name())
		}
	}
}

func previewIgnoringTheGate(t *testing.T, vendor *fake.Provider, env string) {
	t.Helper()
	err := stackrecords.Write(t.Context(), vendor.KeyValues(), environment.TierPreview, "shop", naming.AppStack(env, "api", naming.NewReleaseToken("build", env)), stackrecords.Stack{
		Kind:      provider.StackApp,
		App:       "api",
		Functions: []provider.Function{{Name: "api", Physical: "shop-" + env + "-api", IgnoresGate: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTheStatusOfAProtectedProjectNamesEveryPreviewFunctionThatIgnoresTheGateInsteadOfCallingItLive(t *testing.T) {
	client, vendor := protectedOriginGatedPreview(t)
	previewIgnoringTheGate(t, vendor, "pr-3")

	edges := getProtectionThrough(t, client, fake.KindDirect).GetEdges()

	if len(edges) != 1 || edges[0].GetPublished() {
		t.Fatalf("status edges = %v, want the edge not live: function api of preview pr-3 answers anyone", edges)
	}
	if got := edges[0].GetError(); !strings.Contains(got, "api") || !strings.Contains(got, "pr-3") {
		t.Errorf("status error = %q, want it to name function api of preview pr-3", got)
	}
}

func TestPublishingWhileAPreviewFunctionIgnoresTheGateWritesTheGateAndFailsNamingIt(t *testing.T) {
	client, vendor := protectedOriginGatedPreview(t)
	previewIgnoringTheGate(t, vendor, "pr-3")

	changed, err := changeProtection(t, client, &contractv1.ChangeProtectionRequest{
		Edge:   &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Change: rotatePassword().GetChange(),
	})
	if err != nil {
		t.Fatalf("ChangeProtection() = %v", err)
	}
	result, _, err := publishProtectionThrough(t, client, fake.KindDirect)
	if err != nil {
		t.Fatalf("PublishProtection() = %v", err)
	}

	published := directEdge(vendor).Publications()
	if len(published) == 0 {
		t.Fatal("the edge was published nothing")
	}
	if published := published[len(published)-1]; published.Gate == nil || !published.Gate.HasPassword(changed.GetPassword()) {
		t.Errorf("the edge holds %+v, want the rotated gate: every preview that checks it is protected by it", published.Gate)
	}
	if result.GetSuccess() || !strings.Contains(result.GetError(), "answer anyone") {
		t.Errorf("result = %+v, want a failure that says some previews still answer anyone", result)
	}
	if edges := result.GetProtection().GetEdges(); len(edges) != 1 || edges[0].GetPublished() || !strings.Contains(edges[0].GetError(), "function api of preview pr-3") {
		t.Errorf("edges = %v, want the edge not live, naming function api of preview pr-3", edges)
	}
	if record, _ := recordedGateOf(t, vendor, "shop"); record.Pending {
		t.Error("the record is pending, and the edge took the gate: `ocel protection sync` cannot gate a function that runs no gate")
	}
}
