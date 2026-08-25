package providerkit_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func usePreviewWildcard(t *testing.T, client contractv1connect.ProviderServiceClient, base string, sel *contractv1.EdgeSelection) *progressv1.ResultEvent {
	t.Helper()
	stream, err := client.UsePreviewWildcard(context.Background(), &contractv1.UsePreviewWildcardRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		BaseDomain: base,
		Edge:       sel,
	})
	if err != nil {
		t.Fatalf("UsePreviewWildcard() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatalf("UsePreviewWildcard() error = %v", err)
	}
	return result
}

func seedWildcard(t *testing.T, provider *fake.Provider, held providerkit.Wildcard) {
	t.Helper()
	encoded, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	name := providerkit.WildcardRecord(providerkit.ClassPreview)
	record, err := providerkit.Held(context.Background(), provider.Records(), name)
	if err != nil {
		t.Fatal(err)
	}
	record.Bytes = encoded
	if _, err := provider.Records().Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

func readHeldWildcard(t *testing.T, provider *fake.Provider) providerkit.Wildcard {
	t.Helper()
	record, err := providerkit.Held(context.Background(), provider.Records(), providerkit.WildcardRecord(providerkit.ClassPreview))
	if err != nil {
		t.Fatal(err)
	}
	var held providerkit.Wildcard
	if len(record.Bytes) > 0 {
		if err := json.Unmarshal(record.Bytes, &held); err != nil {
			t.Fatal(err)
		}
	}
	return held
}

func TestUsePreviewWildcardDiscardsTheCertificateItSupersedes(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	provider.IssueCertificates(validationRecord)
	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	provider.RotateCertificates()
	provider.IssueCertificates(rotatedValidationRecord)
	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the rotation settled", result.GetError())
	}

	if discarded := provider.Discarded(); !slices.Contains(discarded, "issued-for-*.preview.acme.com") {
		t.Errorf("the provider discarded %v, want the superseded certificate among them", discarded)
	}
	if held := readHeldWildcard(t, provider); len(held.Settled.Superseded) != 0 {
		t.Errorf("the record still carries %+v, want the discarded certificate forgotten", held.Settled.Superseded)
	}
	if records := provider.DNS().(*fake.DNS).Writer("acme.com").Records(); slices.Contains(records, validationRecord) {
		t.Errorf("the zone still holds %v, want the superseded validation record released", records)
	}
}

func TestUsePreviewWildcardRaisesTheEntryAndRecordsItsHolder(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")

	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	if raised := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Wildcard(); raised != "preview.acme.com" {
		t.Errorf("the %s edge holds %q, want preview.acme.com reconciled on it", fake.KindRelay, raised)
	}
	got, err := client.GetPreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
	})
	if err != nil {
		t.Fatalf("GetPreviewWildcard() error = %v", err)
	}
	held := got.GetWildcard()
	if held.GetBaseDomain() != "preview.acme.com" {
		t.Fatalf("GetPreviewWildcard() = %+v, want the recorded base domain", held)
	}
	if !held.GetRouteInstalled() {
		t.Error("GetPreviewWildcard() says the shared entry route is not installed, though the edge owns it")
	}
	if held.GetGrammarMin() != edge.PreviewGrammarMin || held.GetGrammarMax() != edge.PreviewGrammarMax {
		t.Errorf("grammar = %d..%d, want the contract's %d..%d",
			held.GetGrammarMin(), held.GetGrammarMax(), edge.PreviewGrammarMin, edge.PreviewGrammarMax)
	}
	if len(held.GetCertificate().GetRecordsWritten()) == 0 {
		t.Error("the wildcard names no written record, though a zone was selected")
	}
}

func TestUsePreviewWildcardRefusesASecondDomain(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")
	usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com"))

	if result := usePreviewWildcard(t, client, "other.acme.com", zoned("acme.com")); result.GetSuccess() {
		t.Fatal("UsePreviewWildcard() moved the bootstrap to a second domain without a release")
	}
}

func TestUsePreviewWildcardRefusesRehomingItToAnotherEdge(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")
	usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com"))

	rehome := zoned("acme.com")
	rehome.Kind = string(fake.KindDirect)
	if result := usePreviewWildcard(t, client, "preview.acme.com", rehome); result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() raised a second wildcard at the %s edge", fake.KindDirect)
	}
}

func TestUsePreviewWildcardRefusesAWildcardArgument(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")

	stream, err := client.UsePreviewWildcard(context.Background(), &contractv1.UsePreviewWildcardRequest{
		Tier:       environmentv1.Tier_TIER_PREVIEW,
		BaseDomain: "*.preview.acme.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drain(stream); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("UsePreviewWildcard() = %v, want the wildcard form refused as an invalid argument", err)
	}
}

func TestGetPreviewWildcardNamesTheProjectsServedOnIt(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com"))

	seedStack(t, provider, providerkit.ClassPreview, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{Slug: "shop", Class: providerkit.ClassPreview, GlobalPreview: "preview.acme.com"},
	})
	seedStack(t, provider, providerkit.ClassPreview, "blog", providerkit.EdgeStackState{
		Edge: edge.StackState{Slug: "blog", Class: providerkit.ClassPreview, GlobalPreview: "elsewhere.acme.com"},
	})

	got, err := client.GetPreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
	})
	if err != nil {
		t.Fatalf("GetPreviewWildcard() error = %v", err)
	}
	if !slices.Equal(got.GetProjects(), []string{"shop"}) {
		t.Errorf("GetPreviewWildcard() serves %v, want only the project recorded on this wildcard", got.GetProjects())
	}
}

func TestPlanRemovePreviewWildcardRefusesWhileAProjectStillHasLivePreviews(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com"))
	seedStack(t, provider, providerkit.ClassPreview, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{Slug: "shop", Class: providerkit.ClassPreview, GlobalPreview: "preview.acme.com"},
	})
	seedEnvironment(t, provider, "shop", naming.AppStack("pr-7", "web", naming.NewRelease("b1", "")))

	if _, err := client.PlanRemovePreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
	}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("PlanRemovePreviewWildcard() = %v, want it refused while previews are still served", err)
	}
}

func TestPlanRemovePreviewWildcardNamesWhatGoesAndWhatStays(t *testing.T) {
	t.Parallel()
	client, _ := contractServed(t, "1.0.0")
	usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com"))

	plan, err := client.PlanRemovePreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
	})
	if err != nil {
		t.Fatalf("PlanRemovePreviewWildcard() error = %v", err)
	}
	if plan.GetSubject() != "preview.acme.com" || plan.GetEdgeKind() != string(fake.KindRelay) {
		t.Errorf("PlanRemovePreviewWildcard() = %+v, want it addressed to the recorded holder", plan)
	}
	var deletes, keeps int
	for _, item := range plan.GetGroups() {
		switch item.GetAction() {
		case contractv1.Change_ACTION_DELETE:
			deletes++
		case contractv1.Change_ACTION_KEEP:
			keeps++
		}
	}
	if deletes == 0 || keeps == 0 {
		t.Errorf("the plan removes %d and keeps %d, want it to name both the entry it deletes and what it leaves standing", deletes, keeps)
	}
}

func TestRemovePreviewWildcardTearsItDownAndForgetsIt(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")
	usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com"))

	stream, err := client.RemovePreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
		Edge: zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("RemovePreviewWildcard() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("RemovePreviewWildcard() = %q, want the wildcard released", result.GetError())
	}

	if raised := provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Wildcard(); raised != "" {
		t.Errorf("the %s edge still holds %q", fake.KindRelay, raised)
	}
	if written := provider.DNS().(*fake.DNS).Writer("acme.com").Records(); len(written) != 0 {
		t.Errorf("the zone still holds %v, want the records ocel wrote taken back", written)
	}
	got, err := client.GetPreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetWildcard() != nil {
		t.Errorf("GetPreviewWildcard() still answers %+v", got.GetWildcard())
	}
}

func TestRemovePreviewWildcardRefusesWhenNothingRecordsItsHolder(t *testing.T) {
	t.Parallel()
	client, provider := contractServed(t, "1.0.0")

	seedWildcard(t, provider, providerkit.Wildcard{BaseDomain: "preview.acme.com"})

	stream, err := client.RemovePreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if result.GetSuccess() {
		t.Fatal("RemovePreviewWildcard() tore down a wildcard through a guessed edge")
	}
}
