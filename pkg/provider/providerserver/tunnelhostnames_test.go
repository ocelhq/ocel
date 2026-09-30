package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

func tunneled() *contractv1.EdgeSelection {
	return &contractv1.EdgeSelection{Tunnel: true}
}

func runsTunnels(facts *provider.Facts) { facts.RunsTunnels = true }

func tunnelRelay(t *testing.T, vendor *fake.Provider) *fake.Edge {
	t.Helper()
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	relay.IssuesOriginCertificates()
	relay.RunsTunnels()
	return relay
}

func addHostnameRefusal(t *testing.T, client contractv1connect.ProviderServiceClient, sel *contractv1.EdgeSelection) string {
	t.Helper()
	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []*contractv1.ConfiguredHostname{{Hostname: "app.acme.com", App: "web"}},
		Edge:       sel,
	})
	if err != nil {
		return err.Error()
	}
	result, err := drain(stream)
	if err != nil {
		return err.Error()
	}
	if result.GetSuccess() {
		t.Fatal("AddHostname() succeeded, want it refused")
	}
	return result.GetError()
}

func TestATunnelIsRefusedOnAProviderWhoseOriginRunsNone(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	tunnelRelay(t, vendor)
	deployed(t, vendor, environment.TierProduction, "shop")

	refused := addHostnameRefusal(t, client, tunneled())

	if !strings.Contains(refused, "tunnel") {
		t.Errorf("AddHostname() refused with %q, want the tunnel named: this provider's origin runs no tunnel for an edge to reach it through", refused)
	}
}

func TestATunnelIsRefusedBehindAnEdgeThatOpensNone(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	vendor.WithFacts(runsTunnels)
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).ProxiesRecords()
	deployed(t, vendor, environment.TierProduction, "shop")

	refused := addHostnameRefusal(t, client, tunneled())

	if !strings.Contains(refused, "tunnel") || !strings.Contains(refused, string(fake.KindRelay)) {
		t.Errorf("AddHostname() refused with %q, want the tunnel and the %s edge named: that edge opens no tunnel", refused, fake.KindRelay)
	}
}

func TestAHostnameThroughATunnelIsClaimedWithoutTrustingOrCertifyingTheOrigin(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	vendor.WithFacts(runsTunnels)
	relay := tunnelRelay(t, vendor)
	deployed(t, vendor, environment.TierProduction, "shop")

	addWebHostname(t, client, "app.acme.com", tunneled())

	claims := relay.Claims()
	if len(claims) != 1 || claims[0].Tunnel != fake.KindRelay || len(claims[0].ClientCertificates) != 0 || claims[0].OriginCertificate.ID != "" {
		t.Fatalf("the router took claims %+v, want one claim through the %s edge's tunnel, trusting no client certificate and carrying no origin certificate", claims, fake.KindRelay)
	}
	if events := relay.ClientCertificateEvents(); slices.Contains(events, "ensure") || slices.Contains(events, "present") {
		t.Errorf("the edge saw %v, want no client certificate read or presented: nothing but the tunnel reaches the origin", events)
	}
	bindings := relay.Bindings()
	if len(bindings) != 1 || bindings[0].Origin == nil || !bindings[0].Origin.Tunneled {
		t.Fatalf("the edge was bound with %+v, want app.acme.com forwarded to the tunnel the claim named", bindings)
	}
	hostState := readStack(t, vendor, environment.TierProduction, "shop").Host("app.acme.com")
	if hostState.OriginCertificateID != "" || len(hostState.ClientCertificateDigests) != 0 || !hostState.Tunneled {
		t.Errorf("the hostname records %+v, want it recorded as tunneled, with no origin or client certificate", hostState)
	}
}

func TestAServedHostnameMovedOntoATunnelIsClaimedAgainAndItsOriginCertificateRevoked(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	vendor.WithFacts(runsTunnels)
	relay := tunnelRelay(t, vendor)
	deployed(t, vendor, environment.TierProduction, "shop")
	addWebHostname(t, client, "app.acme.com", nil)

	addWebHostname(t, client, "app.acme.com", tunneled())

	claims := relay.Claims()
	if len(claims) == 0 || claims[len(claims)-1].Tunnel != fake.KindRelay {
		t.Fatalf("the router took claims %+v, want app.acme.com claimed again through the tunnel", claims)
	}
	if revoked := relay.RevokedOriginCertificates(); !slices.Equal(revoked, []string{"origin-certificate-1"}) {
		t.Errorf("the edge revoked %v, want origin-certificate-1: the origin answers the tunnel with no certificate the edge issued", revoked)
	}
	if bindings := relay.Bindings(); !bindings[len(bindings)-1].Origin.Tunneled {
		t.Errorf("the edge was last bound with %+v, want the tunnel", bindings[len(bindings)-1])
	}
}

func TestAHostnameThroughATunnelIsNotClaimedAgainWhenNothingChanged(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	vendor.WithFacts(runsTunnels)
	relay := tunnelRelay(t, vendor)
	deployed(t, vendor, environment.TierProduction, "shop")
	addWebHostname(t, client, "app.acme.com", tunneled())

	addWebHostname(t, client, "app.acme.com", tunneled())

	if claims := relay.Claims(); len(claims) != 1 {
		t.Errorf("the router took %d claims, want the one: the tunnel still reaches the origin", len(claims))
	}
	if events := relay.ClientCertificateEvents(); slices.Contains(events, "ensure") {
		t.Errorf("the edge saw %v, want no client certificate read for a tunneled hostname", events)
	}
}

func TestTheSharedPreviewWildcardThroughATunnelIsClaimedOnTheTunnelWithNoCertificates(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	vendor.WithFacts(runsTunnels)
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindDirect)
	relay.ProxiesRecords()
	relay.IssuesOriginCertificates()
	relay.RunsTunnels()

	if result := usePreviewWildcard(t, client, "preview.acme.com", &contractv1.EdgeSelection{Kind: string(fake.KindDirect), Tunnel: true}); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want previews forwarded through the tunnel", result.GetError())
	}

	entries := relay.PreviewEntryClaims()
	if len(entries) != 1 || entries[0].Tunnel != fake.KindDirect || len(entries[0].ClientCertificates) != 0 || entries[0].OriginCertificate.ID != "" {
		t.Fatalf("the router took preview entry claims %+v, want *.preview.acme.com claimed once through the tunnel, with no certificate", entries)
	}
	specs := relay.Specs()
	if last := specs[len(specs)-1]; last.Origin == nil || !last.Origin.Tunneled {
		t.Errorf("the edge reconciled the wildcard forwarding to %+v, want the tunnel", last.Origin)
	}
	if recorded := readRecordedWildcard(t, vendor).Host; !recorded.Tunneled || recorded.OriginCertificateID != "" {
		t.Errorf("the wildcard records %+v, want it tunneled, with no origin certificate", recorded)
	}
}

func TestAProjectsOwnPreviewWildcardThroughATunnelIsClaimedOnTheTunnel(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	vendor.WithFacts(runsTunnels)
	previewBootstrapped(t, client)
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindDirect)
	relay.ProxiesRecords()
	relay.RunsTunnels()
	req := previewRequest()
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindDirect), Tunnel: true}

	if result, _ := deploy(t, client, req); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want the preview forwarded through the tunnel", result.GetError())
	}

	at := slices.IndexFunc(relay.Claims(), func(claim router.Claim) bool { return claim.Hostname == "*.preview.example" })
	if at < 0 || relay.Claims()[at].Tunnel != fake.KindDirect {
		t.Errorf("the router took claims %+v, want *.preview.example claimed through the tunnel", relay.Claims())
	}
}
