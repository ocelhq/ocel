package providerserver_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

func addWebHostname(t *testing.T, client contractv1connect.ProviderServiceClient, host string, sel *contractv1.EdgeSelection) {
	t.Helper()
	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []*contractv1.ConfiguredHostname{{Hostname: host, App: "web"}},
		Edge:       sel,
	})
	if err != nil {
		t.Fatalf("AddHostname() error = %v", err)
	}
	result, err := drain(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("AddHostname() = %q, want the hostname attached", result.GetError())
	}
}

func TestAHostnameAnEdgeProxiesIsClaimedOnTheRouterAndForwardedToTheOriginTheClaimNames(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	vendor.PinServingCertificate("app.acme.com", "pinned-app-certificate")
	deployed(t, vendor, environment.TierProduction, "shop")

	addWebHostname(t, client, "app.acme.com", nil)

	claims := relay.Claims()
	want := router.Claim{Hostname: "app.acme.com", App: "web", Certificate: "pinned-app-certificate", ClientCertificate: fake.ClientCertificate(fake.KindRelay)}
	if len(claims) != 1 || claims[0] != want {
		t.Fatalf("the router took claims %+v, want the one %+v: an edge that proxies records forwards to an origin, and the router is what answers there", claims, want)
	}
	bindings := relay.Bindings()
	if len(bindings) != 1 || bindings[0].Origin == nil || *bindings[0].Origin != fake.Origin(fake.RouterRelay) {
		t.Fatalf("the edge was bound with %+v, want app.acme.com forwarded to %+v, the origin the claim named", bindings, fake.Origin(fake.RouterRelay))
	}
	hostState := readStack(t, vendor, environment.TierProduction, "shop").Host("app.acme.com")
	if len(hostState.Manual) != 0 {
		t.Errorf("the hostname owes manual records %v, want none: the edge wrote the proxied record itself when it bound the hostname", hostState.Manual)
	}
	if !hostState.Probe.OK {
		t.Errorf("recorded probe = %+v, want the hostname answered", hostState.Probe)
	}
}

func TestAHostnameAnEdgeDoesNotProxyIsBoundWithNoOriginAndNoClaim(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	deployed(t, vendor, environment.TierProduction, "shop")

	addWebHostname(t, client, "app.acme.com", zoned("acme.com"))

	if claims := relay.Claims(); len(claims) != 0 {
		t.Errorf("the router took claims %+v, want none: the edge answers the hostname itself", claims)
	}
	if bindings := relay.Bindings(); len(bindings) != 1 || bindings[0].Origin != nil {
		t.Errorf("the edge was bound with %+v, want one binding naming no origin", bindings)
	}
}

func TestRemovingAHostnameAnEdgeProxiesGivesTheClaimBackToTheRouter(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	deployed(t, vendor, environment.TierProduction, "shop")
	addWebHostname(t, client, "app.acme.com", nil)

	remove, err := client.RemoveHostname(context.Background(), &contractv1.HostnameRequest{Slug: "shop", Host: "app.acme.com"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := drain(remove)
	if err != nil {
		t.Fatal(err)
	}
	if !result.GetSuccess() {
		t.Fatalf("RemoveHostname() = %q, want the hostname given back", result.GetError())
	}
	if disclaimed := relay.Disclaimed(); !slices.Equal(disclaimed, []string{"app.acme.com"}) {
		t.Errorf("the router gave back %v, want app.acme.com: the hostname stops answering at the origin once no edge forwards it", disclaimed)
	}
	if bound := readStack(t, vendor, environment.TierProduction, "shop").Edge.Bound; slices.Contains(bound, "app.acme.com") {
		t.Errorf("the edge still binds %v", bound)
	}
}

func TestTheRemovalPlanOfAProjectAnEdgeProxiesNamesWhatItsRouterTakesDown(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	deployed(t, vendor, environment.TierProduction, "shop")
	addWebHostname(t, client, "app.acme.com", nil)

	plan, err := client.PlanRemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatalf("PlanRemoveProject() error = %v", err)
	}
	var claimed []string
	for _, group := range plan.GetGroups() {
		for _, change := range group.GetChanges() {
			if change.GetKind() == fake.ClaimKind {
				claimed = append(claimed, change.GetName())
			}
		}
	}
	if !slices.Equal(claimed, []string{"app.acme.com"}) {
		t.Errorf("the removal plan names claims %v, want app.acme.com: the router the edge forwards to takes its claim down with the project, and a plan names everything the removal deletes", claimed)
	}
}
