package providerserver_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestTheSharedPreviewWildcardAnEdgeProxiesIsForwardedToThePreviewEntryItsRouterClaims(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindDirect)
	relay.ProxiesRecords()
	relay.IssuesOriginCertificates()

	if result := usePreviewWildcard(t, client, "preview.acme.com", &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want previews forwarded through the edge", result.GetError())
	}

	entries := relay.PreviewEntryClaims()
	if len(entries) != 2 || entries[0].Hostname != "*.preview.acme.com" || !slices.Equal(entries[0].ClientCertificates, []string{fake.ClientCertificate(fake.KindDirect)}) {
		t.Fatalf("the router took preview entry claims %+v, want *.preview.acme.com claimed trusting the client certificate the edge presents", entries)
	}
	if entries[1].OriginCertificate.ID != "origin-certificate-1" {
		t.Errorf("the router was claimed again with %+v, want the origin certificate the edge issued for the wildcard", entries[1].OriginCertificate)
	}
	specs := relay.Specs()
	if last := specs[len(specs)-1]; last.Origin == nil || *last.Origin != fake.Origin(fake.RouterDirect) {
		t.Errorf("the edge reconciled the wildcard forwarding to %+v, want the origin the router's preview entry answers on", last.Origin)
	}
	if recorded := readRecordedWildcard(t, vendor).Host.OriginCertificate; recorded != "origin-certificate-1" {
		t.Errorf("the wildcard records origin certificate %q, want origin-certificate-1", recorded)
	}

	stream, err := client.RemovePreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemovePreviewWildcard() = %q, %v", result.GetError(), err)
	}
	if released := relay.DisclaimedPreviewEntries(); !slices.Equal(released, []string{"preview.acme.com"}) {
		t.Errorf("the router gave back preview entries %v, want preview.acme.com: nothing forwards previews there any more", released)
	}
	if revoked := relay.RevokedOriginCertificates(); !slices.Equal(revoked, []string{"origin-certificate-1"}) {
		t.Errorf("the edge revoked %v, want the wildcard's origin certificate", revoked)
	}
}

func TestAProjectsOwnPreviewWildcardAnEdgeProxiesIsClaimedOnTheRouterAndForwardedToIt(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindDirect)
	relay.ProxiesRecords()
	req := previewRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the preview forwarded through the edge", result.GetError())
	}

	if !slices.ContainsFunc(relay.Claims(), func(claim router.Claim) bool { return claim.Hostname == "*.preview.example" }) {
		t.Errorf("the router took claims %+v, want *.preview.example: the router behind the edge answers every preview under it", relay.Claims())
	}
	bound := slices.IndexFunc(relay.Bindings(), func(binding edge.DomainBinding) bool { return binding.Hostname == "*.preview.example" })
	if bound < 0 || relay.Bindings()[bound].Origin == nil {
		t.Errorf("the edge was bound with %+v, want *.preview.example forwarded to the origin its claim named", relay.Bindings())
	}
}

func TestAnEdgeThatRunsTheCodeItProxiesToServesThePreviewWildcardItselfWithNoClaim(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()

	if result := usePreviewWildcard(t, client, "preview.acme.com", nil); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}
	if entries := relay.PreviewEntryClaims(); len(entries) != 0 {
		t.Errorf("the router took preview entry claims %+v, want none: the edge runs the preview entry itself", entries)
	}
	if specs := relay.Specs(); len(specs) != 1 || specs[0].Program == nil || specs[0].Origin != nil {
		t.Errorf("the edge reconciled the wildcard with %+v, want its program and no origin", specs)
	}
}

func TestTheReleasePlanOfAPreviewWildcardAnEdgeForwardsNamesWhatItsRouterTakesDown(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	direct := vendor.Edges().(*fake.Edges).Edge(fake.KindDirect)
	direct.ProxiesRecords()
	if result := usePreviewWildcard(t, client, "preview.acme.com", &contractv1.EdgeSelection{Kind: string(fake.KindDirect)}); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}

	plan, err := client.PlanRemovePreviewWildcard(context.Background(), &contractv1.PreviewWildcardRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("PlanRemovePreviewWildcard() error = %v", err)
	}
	var entries []string
	for _, group := range plan.GetGroups() {
		for _, change := range group.GetChanges() {
			if change.GetKind() == fake.PreviewEntryClaimKind {
				entries = append(entries, change.GetName())
			}
		}
	}
	if !slices.Equal(entries, []string{"*.preview.acme.com"}) {
		t.Errorf("the release plan names preview entries %v, want *.preview.acme.com: the router the edge forwards previews to takes its entry down", entries)
	}
}
