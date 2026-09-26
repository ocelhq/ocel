package providerserver_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
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

func seedWildcard(t *testing.T, vendor *fake.Provider, wildcard stackrecords.Wildcard) {
	t.Helper()
	encoded, err := json.Marshal(wildcard)
	if err != nil {
		t.Fatal(err)
	}
	name := stackrecords.WildcardRecord(edge.ClassPreview)
	record, err := records.ReadOrEmpty(context.Background(), vendor.Records(), name)
	if err != nil {
		t.Fatal(err)
	}
	record.Bytes = encoded
	if _, err := vendor.Records().Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
}

func readRecordedWildcard(t *testing.T, vendor *fake.Provider) stackrecords.Wildcard {
	t.Helper()
	record, err := records.ReadOrEmpty(context.Background(), vendor.Records(), stackrecords.WildcardRecord(edge.ClassPreview))
	if err != nil {
		t.Fatal(err)
	}
	var wildcard stackrecords.Wildcard
	if len(record.Bytes) > 0 {
		if err := json.Unmarshal(record.Bytes, &wildcard); err != nil {
			t.Fatal(err)
		}
	}
	return wildcard
}

func TestUsePreviewWildcardDiscardsTheCertificateItSupersedes(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	vendor.RequireValidationRecords(validationRecord)
	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	vendor.RotateCertificates()
	vendor.RequireValidationRecords(rotatedValidationRecord)
	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the rotation cut over", result.GetError())
	}

	if discarded := vendor.Discarded(); !slices.Contains(discarded, "issued-for-*.preview.acme.com") {
		t.Errorf("the provider discarded %v, want the superseded certificate among them", discarded)
	}
	if wildcard := readRecordedWildcard(t, vendor); len(wildcard.Host.Superseded) != 0 {
		t.Errorf("the record still has %+v, want the discarded certificate forgotten", wildcard.Host.Superseded)
	}
	if dnsRecords := vendor.DNS().(*fake.DNS).Zone("acme.com").Records(); slices.Contains(dnsRecords, validationRecord) {
		t.Errorf("the zone still contains %v, want the superseded validation record released", dnsRecords)
	}
}

func TestUsePreviewWildcardRaisesTheEntryAndRecordsItsOwningEdge(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")

	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	if raised := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Wildcard(); raised != "preview.acme.com" {
		t.Errorf("the %s edge serves %q, want preview.acme.com reconciled on it", fake.KindRelay, raised)
	}
	got, err := client.GetPreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
	})
	if err != nil {
		t.Fatalf("GetPreviewWildcard() error = %v", err)
	}
	wildcard := got.GetWildcard()
	if wildcard.GetBaseDomain() != "preview.acme.com" {
		t.Fatalf("GetPreviewWildcard() = %+v, want the recorded base domain", wildcard)
	}
	if !wildcard.GetRouteInstalled() {
		t.Error("GetPreviewWildcard() says the shared entry route is not installed, though the edge owns it")
	}
	if wildcard.GetGrammarMin() != edge.PreviewGrammarMin || wildcard.GetGrammarMax() != edge.PreviewGrammarMax {
		t.Errorf("grammar = %d..%d, want the contract's %d..%d",
			wildcard.GetGrammarMin(), wildcard.GetGrammarMax(), edge.PreviewGrammarMin, edge.PreviewGrammarMax)
	}
	if len(wildcard.GetCertificate().GetRecordsWritten()) == 0 {
		t.Error("the wildcard names no written record, though a zone was selected")
	}
}

func TestUsePreviewWildcardOpensItsDNSForTheEdgeTheSelectionNames(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")

	selected := zoned("acme.com")
	selected.Kind = string(fake.KindRelay)
	usePreviewWildcard(t, client, "preview.acme.com", selected)

	if fronts := vendor.DNS().(*fake.DNS).Fronts(); !slices.Equal(fronts, []edge.Kind{fake.KindRelay}) {
		t.Errorf("the DNS was opened under %v, want the %s edge the selection names", fronts, fake.KindRelay)
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
	client, vendor := contractServed(t, "1.0.0")
	usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com"))

	seedStack(t, vendor, edge.ClassPreview, "shop", stackrecords.EdgeState{
		Edge: edge.StackState{Slug: "shop", Class: edge.ClassPreview, GlobalPreview: "preview.acme.com"},
	})
	seedStack(t, vendor, edge.ClassPreview, "blog", stackrecords.EdgeState{
		Edge: edge.StackState{Slug: "blog", Class: edge.ClassPreview, GlobalPreview: "elsewhere.acme.com"},
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
	client, vendor := contractServed(t, "1.0.0")
	usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com"))
	seedStack(t, vendor, edge.ClassPreview, "shop", stackrecords.EdgeState{
		Edge: edge.StackState{Slug: "shop", Class: edge.ClassPreview, GlobalPreview: "preview.acme.com"},
	})
	seedEnvironment(t, vendor, "shop", naming.AppStack("pr-7", "web", naming.NewRelease("b1", "")))

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
		t.Errorf("PlanRemovePreviewWildcard() = %+v, want it addressed to the recorded owning edge", plan)
	}
	var deletes, keeps int
	for _, item := range plan.GetGroups() {
		switch item.GetAction() {
		case planv1.Change_ACTION_DELETE:
			deletes++
		case planv1.Change_ACTION_KEEP:
			keeps++
		}
	}
	if deletes == 0 || keeps == 0 {
		t.Errorf("the plan removes %d and keeps %d, want it to name both the entry it deletes and what it leaves in place", deletes, keeps)
	}
}

func TestRemovePreviewWildcardTearsItDownAndForgetsIt(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
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

	if raised := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Wildcard(); raised != "" {
		t.Errorf("the %s edge still serves %q", fake.KindRelay, raised)
	}
	if written := vendor.DNS().(*fake.DNS).Zone("acme.com").Records(); len(written) != 0 {
		t.Errorf("the zone still contains %v, want the records ocel wrote taken back", written)
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

func TestRemovePreviewWildcardOpensItsDNSForTheEdgeThatOwnsIt(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	selection := zoned("acme.com")
	selection.Kind = string(fake.KindDirect)
	if result := usePreviewWildcard(t, client, "preview.acme.com", selection); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}
	opened := len(vendor.DNS().(*fake.DNS).Fronts())

	stream, err := client.RemovePreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
		Edge: zoned("acme.com"),
	})
	if err != nil {
		t.Fatalf("RemovePreviewWildcard() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemovePreviewWildcard() = %q, %v", result.GetError(), err)
	}
	fronts := vendor.DNS().(*fake.DNS).Fronts()[opened:]
	if !slices.Equal(fronts, []edge.Kind{fake.KindDirect}) {
		t.Errorf("the release opened its DNS under %v, want the %s edge that owns the wildcard", fronts, fake.KindDirect)
	}
}

func TestRemovePreviewWildcardRefusesWhenNothingRecordsItsOwningEdge(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")

	seedWildcard(t, vendor, stackrecords.Wildcard{BaseDomain: "preview.acme.com"})

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

func TestThePreviewWildcardNamesWhoRenewsItAndWhenItExpires(t *testing.T) {
	t.Parallel()
	client, p := contractServed(t, "1.0.0")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
	seedWildcard(t, p, stackrecords.Wildcard{
		BaseDomain: "preview.acme.com",
		Edge:       fake.KindRelay,
		Host:       stackrecords.HostnameState{Certificate: provider.Certificate{ID: "pem:/etc/ocel/preview/certs/wildcard"}},
	})
	expiry := time.Now().Add(9 * 24 * time.Hour).Unix()
	p.ReportCertificate(provider.CertificateHealth{
		Terminates:   true,
		Status:       "PINNED",
		Renewal:      "you placed it on this box and you renew it",
		ExpiresAt:    expiry,
		ExpiringSoon: true,
	})

	got, err := client.GetPreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{
		Tier: environmentv1.Tier_TIER_PREVIEW,
	})
	if err != nil {
		t.Fatalf("GetPreviewWildcard() error = %v", err)
	}
	assertWildcardRenewal(t, "GetPreviewWildcard()", got.GetWildcard(), expiry)

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PREVIEW,
		Slug:         "shop",
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	assertWildcardRenewal(t, "Preflight()", resp.GetPreviewWildcard(), expiry)
}

func assertWildcardRenewal(t *testing.T, what string, wildcard *contractv1.PreviewWildcard, expiry int64) {
	t.Helper()
	if wildcard.GetBaseDomain() != "preview.acme.com" {
		t.Fatalf("%s wildcard = %+v, want the seeded base domain", what, wildcard)
	}
	if wildcard.GetRenewalStatus() == "" {
		t.Errorf("%s says nothing about who renews the wildcard, and a pinned pair is the one certificate nothing on the box renews", what)
	}
	if wildcard.GetExpiresAt() != expiry {
		t.Errorf("%s expires at %d, want %d", what, wildcard.GetExpiresAt(), expiry)
	}
	if !wildcard.GetExpiringSoon() {
		t.Errorf("%s does not call a wildcard nine days out expiring soon", what)
	}
}
