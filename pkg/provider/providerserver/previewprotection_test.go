package providerserver_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
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

func lastPublication(t *testing.T, vendor *fake.Provider) edge.PreviewGatePublication {
	t.Helper()
	published := relayEdge(vendor).Publications()
	if len(published) == 0 {
		t.Fatal("the edge was published nothing")
	}
	return published[len(published)-1]
}

func changeProtection(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ChangePreviewProtectionRequest) (*contractv1.ChangePreviewProtectionResponse, error) {
	t.Helper()
	if req.Slug == "" {
		req.Slug = "shop"
	}
	if req.Edge == nil {
		req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	}
	return client.ChangePreviewProtection(context.Background(), req)
}

func publishProtection(t *testing.T, client contractv1connect.ProviderServiceClient) (*progressv1.OperationResult, []*progressv1.OperationEvent, error) {
	t.Helper()
	stream, err := client.PublishPreviewProtection(context.Background(), &contractv1.PublishPreviewProtectionRequest{
		Slug: "shop",
		Edge: &contractv1.EdgeSelection{Kind: string(fake.KindRelay)},
	})
	if err != nil {
		t.Fatalf("PublishPreviewProtection() error = %v", err)
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

func changeAndPublish(t *testing.T, client contractv1connect.ProviderServiceClient, req *contractv1.ChangePreviewProtectionRequest) (*contractv1.ChangePreviewProtectionResponse, *progressv1.OperationResult) {
	t.Helper()
	changed, err := changeProtection(t, client, req)
	if err != nil {
		t.Fatalf("ChangePreviewProtection() = %v", err)
	}
	result, _, err := publishProtection(t, client)
	if err != nil {
		t.Fatalf("PublishPreviewProtection() = %v", err)
	}
	return changed, result
}

func rotatePassword() *contractv1.ChangePreviewProtectionRequest {
	return &contractv1.ChangePreviewProtectionRequest{Change: &contractv1.ChangePreviewProtectionRequest_RotatePassword{RotatePassword: &contractv1.RotatePreviewPassword{}}}
}

func setPassword(password string) *contractv1.ChangePreviewProtectionRequest {
	return &contractv1.ChangePreviewProtectionRequest{Change: &contractv1.ChangePreviewProtectionRequest_SetPassword{SetPassword: &contractv1.SetPreviewPassword{Password: password}}}
}

func createBypass() *contractv1.ChangePreviewProtectionRequest {
	return &contractv1.ChangePreviewProtectionRequest{Change: &contractv1.ChangePreviewProtectionRequest_CreateBypassSecret{CreateBypassSecret: &contractv1.CreatePreviewBypassSecret{}}}
}

func revokeBypass(id string) *contractv1.ChangePreviewProtectionRequest {
	return &contractv1.ChangePreviewProtectionRequest{Change: &contractv1.ChangePreviewProtectionRequest_RevokeBypassSecret{RevokeBypassSecret: &contractv1.RevokePreviewBypassSecret{Id: id}}}
}

func getProtection(t *testing.T, client contractv1connect.ProviderServiceClient, reveal bool) *contractv1.GetPreviewProtectionResponse {
	t.Helper()
	resp, err := client.GetPreviewProtection(context.Background(), &contractv1.GetPreviewProtectionRequest{Slug: "shop", RevealPassword: reveal, Edge: &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}})
	if err != nil {
		t.Fatalf("GetPreviewProtection() = %v", err)
	}
	return resp
}

func TestAProjectWithNoRecordedGateReportsNoneAndRevealsNothing(t *testing.T) {
	client, _ := contractServed(t, "1.0.0")

	resp := getProtection(t, client, true)

	if resp.GetRecorded() || resp.GetPassword() != "" || len(resp.GetBypassSecretIds()) != 0 || len(resp.GetEdges()) != 0 {
		t.Errorf("GetPreviewProtection() = recorded %t, password revealed %t, bypass ids %v, edges %v, want an empty answer: nothing is recorded for shop",
			resp.GetRecorded(), resp.GetPassword() != "", resp.GetBypassSecretIds(), resp.GetEdges())
	}
}

func TestTheStatusOfAProtectedPreviewNamesItsEdgeAndHidesThePasswordUnlessAskedFor(t *testing.T) {
	client, vendor := protectedPreview(t)
	record, _ := recordedGateOf(t, vendor, "shop")

	hidden := getProtection(t, client, false)
	revealed := getProtection(t, client, true)

	if !hidden.GetRecorded() || hidden.GetPassword() != "" {
		t.Errorf("status = recorded %t, password revealed %t, want the gate recorded and the password left out", hidden.GetRecorded(), hidden.GetPassword() != "")
	}
	if revealed.GetPassword() != record.Password {
		t.Errorf("revealed password = %q, want the recorded one", revealed.GetPassword())
	}
	if edges := revealed.GetEdges(); len(edges) != 1 || edges[0].GetKind() != string(fake.KindRelay) || !edges[0].GetPublished() {
		t.Errorf("edges = %v, want the relay edge, live: the deploy that created the gate wrote it there", edges)
	}
}

func TestRotatingThePasswordRecordsAndPublishesAGateTheOldPasswordAndSessionsFailAgainst(t *testing.T) {
	client, vendor := protectedPreview(t)
	before, _ := recordedGateOf(t, vendor, "shop")

	changed, result := changeAndPublish(t, client, rotatePassword())

	if !result.GetSuccess() {
		t.Fatalf("PublishPreviewProtection() = %v, want it to succeed", result)
	}
	after, _ := recordedGateOf(t, vendor, "shop")
	generated := changed.GetPassword()
	if generated == "" || generated == before.Password || after.Password != generated {
		t.Errorf("the change answered password %q, the record %q, was %q, want a new one in both", generated, after.Password, before.Password)
	}
	if after.Gate.Key == before.Gate.Key || after.Gate.PasswordMAC != previewgate.HashPassword(after.Gate.Key, generated) {
		t.Errorf("the gate is %+v after the rotation, want a new key and the new password's MAC under it, so every session ends", after.Gate)
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
		t.Fatalf("PublishPreviewProtection() = %v, %v", result, err)
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
		t.Fatalf("PublishPreviewProtection() = %v", result)
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
		t.Errorf("ChangePreviewProtection() = %v, want an invalid-argument refusal", err)
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
		t.Fatalf("PublishPreviewProtection() = %v", result)
	}
	secret, id := changed.GetBypassSecret(), changed.GetBypassSecretId()
	after, _ := recordedGateOf(t, vendor, "shop")
	if secret == "" || len(id) != 12 || len(after.Gate.BypassSecrets) != 1 || after.Gate.BypassSecrets[0] != secret {
		t.Errorf("secret %q id %q record %+v, want one secret recorded under the id returned", secret, id, after.Gate)
	}
	if after.Gate.Key != before.Gate.Key {
		t.Error("creating a bypass secret rotated the key, and that ends every session for nothing")
	}
	if listed := getProtection(t, client, false).GetBypassSecretIds(); len(listed) != 1 || listed[0] != id {
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
		t.Fatalf("PublishPreviewProtection() = %v", result)
	}
	after, _ := recordedGateOf(t, vendor, "shop")
	if len(after.Gate.BypassSecrets) != 0 || after.Gate.Key == before.Gate.Key {
		t.Errorf("the gate is %+v, want the secret gone and a new key so the sessions it opened end", after.Gate)
	}
	if after.Gate.PasswordMAC != previewgate.HashPassword(after.Gate.Key, after.Password) {
		t.Error("the password no longer matches the gate after the key rotated")
	}
}

func TestRevokingAnUnknownBypassSecretIsRefused(t *testing.T) {
	client, _ := protectedPreview(t)

	_, err := changeProtection(t, client, revokeBypass("0123456789ab"))

	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "ocel preview protection bypass ls") {
		t.Errorf("ChangePreviewProtection() = %v, want a refusal that points at `ocel preview protection bypass ls`", err)
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

	if result.GetSuccess() || !strings.Contains(result.GetError(), "ocel preview protection sync") {
		t.Errorf("result = %+v, want a failure that names `ocel preview protection sync`", result)
	}
	if record, _ := recordedGateOf(t, vendor, "shop"); record.Password != changed.GetPassword() || !record.Pending {
		t.Errorf("the record is %+v, want the new password recorded and pending", record)
	}
	if edges := result.GetProtection().GetEdges(); len(edges) != 1 || edges[0].GetPublished() || !strings.Contains(edges[0].GetError(), "unreachable") {
		t.Errorf("edges = %v, want the relay edge, not published, with the reason", edges)
	}
	if edges := getProtection(t, client, false).GetEdges(); len(edges) != 1 || edges[0].GetPublished() {
		t.Errorf("status edges = %v, want the relay edge pending", edges)
	}
}

func TestAChangeBeforeAnyPreviewHasDeployedRecordsTheGateAndHasNoEdgeToPublishTo(t *testing.T) {
	client, vendor := contractServed(t, "1.0.0")

	_, result := changeAndPublish(t, client, createBypass())

	if !result.GetSuccess() {
		t.Fatalf("PublishPreviewProtection() = %v, want it to succeed: the first preview deploy publishes what is recorded", result)
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

	_, err := changeProtection(t, client, &contractv1.ChangePreviewProtectionRequest{
		Edge:   &contractv1.EdgeSelection{Kind: string(fake.KindDirect)},
		Change: rotatePassword().Change,
	})

	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("ChangePreviewProtection() = %v, want a refusal: the project's previews are served through the relay edge", err)
	}
}

func TestAChangeWhileTheLastDeployOpenedThePreviewsIsRefusedAndKeepsTheCredentials(t *testing.T) {
	client, vendor := protectedPreview(t)
	before, _ := recordedGateOf(t, vendor, "shop")
	if result, _ := deploy(t, client, previewDeployRequest()); !result.GetSuccess() {
		t.Fatalf("unprotected Deploy() = %q", result.GetError())
	}

	_, err := changeProtection(t, client, rotatePassword())

	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "previews.protection") {
		t.Errorf("ChangePreviewProtection() = %v, want a refusal naming previews.protection", err)
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
	if said := err; said == nil || !strings.Contains(said.Error(), "previews.protection") {
		t.Errorf("sync error = %v, want it to name previews.protection and the deploy that restores protection", err)
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
	relayEdge(vendor).GatesPreviews(false)
	if result, _ := deploy(t, client, previewDeployRequest()); !result.GetSuccess() {
		t.Fatalf("unprotected Deploy() = %q", result.GetError())
	}
	if _, err := changeProtection(t, client, createBypass()); err != nil {
		t.Fatal(err)
	}

	result, _, err := publishProtection(t, client)

	if err != nil || result.GetSuccess() {
		t.Fatalf("PublishPreviewProtection() = %v, %v, want the failure in the result", result, err)
	}
	if edges := result.GetProtection().GetEdges(); len(edges) != 1 || !strings.Contains(edges[0].GetError(), "runs no preview gate") {
		t.Errorf("edges = %v, want the relay edge, saying it runs no preview gate rather than that no preview deployed", edges)
	}
	if edges := getProtection(t, client, false).GetEdges(); len(edges) != 1 || edges[0].GetPublished() || !strings.Contains(edges[0].GetError(), "runs no preview gate") {
		t.Errorf("status edges = %v, want the relay edge, saying it runs no preview gate", edges)
	}
}

func TestEveryResponseCarryingThePreviewPasswordMarksItDebugRedactSoRedactvetRefusesToPrintTheResponse(t *testing.T) {
	for _, message := range []proto.Message{&contractv1.GetPreviewProtectionResponse{}, &contractv1.ChangePreviewProtectionResponse{}} {
		descriptor := message.ProtoReflect().Descriptor()
		field := descriptor.Fields().ByName("password")
		if options, _ := field.Options().(*descriptorpb.FieldOptions); !options.GetDebugRedact() {
			t.Errorf("%s.password is not marked debug_redact, want it marked so redactvet refuses to print the response", descriptor.Name())
		}
	}
}
