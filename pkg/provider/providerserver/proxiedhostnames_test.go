package providerserver_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

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
	want := router.Claim{Hostname: "app.acme.com", App: "web", Certificate: "pinned-app-certificate", ClientCAs: []string{fake.ClientCA(fake.KindRelay)}}
	if len(claims) != 1 || !reflect.DeepEqual(claims[0], want) {
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

func TestAnEdgePresentsAClientCertificateOnlyOnceTheRouterTookTheClaimThatTrustsIt(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	deployed(t, vendor, environment.TierProduction, "shop")

	addWebHostname(t, client, "app.acme.com", nil)

	if got := relay.ClientCertificateEvents(); !slices.Equal(got, []string{"ensure", "claim", "present", "ensure"}) {
		t.Errorf("the edge and router saw %v, want the certificates the edge holds read, claimed on the router, then presented: an origin refuses a certificate it was not told to trust", got)
	}
}

func TestAServedHostnameIsClaimedAgainWhenTheCertificatesItsEdgePresentsChange(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	deployed(t, vendor, environment.TierProduction, "shop")
	addWebHostname(t, client, "app.acme.com", nil)

	addWebHostname(t, client, "app.acme.com", nil)
	if claims := relay.Claims(); len(claims) != 1 {
		t.Fatalf("the router took %d claims, want the one: nothing the origin trusts changed", len(claims))
	}

	rotated := []string{fake.ClientCA(fake.KindRelay), "the certificate you uploaded beside it"}
	relay.HoldsClientCAs(rotated...)
	addWebHostname(t, client, "app.acme.com", nil)
	claims := relay.Claims()
	if len(claims) != 2 || !slices.Equal(claims[1].ClientCAs, rotated) {
		t.Fatalf("the router took claims %+v, want app.acme.com claimed again trusting %v: the edge presents the certificate uploaded last, and the origin refuses it until it is told to trust it", claims, rotated)
	}
}

func TestAnOriginThatHoldsNoCertificateForAForwardedHostnameIsIssuedOneByTheEdge(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	relay.IssuesOriginCertificates()
	deployed(t, vendor, environment.TierProduction, "shop")

	addWebHostname(t, client, "app.acme.com", nil)

	claims := relay.Claims()
	if len(claims) != 2 || claims[0].OriginCertificate.ID != "" || claims[1].OriginCertificate.ID != "origin-certificate-1" {
		t.Fatalf("the router took claims %+v, want the hostname claimed, then claimed again with the certificate the edge issued once the origin said it holds none", claims)
	}
	if recorded := readStack(t, vendor, environment.TierProduction, "shop").Host("app.acme.com").OriginCertificateID; recorded != "origin-certificate-1" {
		t.Errorf("the hostname records origin certificate %q, want origin-certificate-1: it is revoked once nothing answers with it", recorded)
	}

	remove, err := client.RemoveHostname(context.Background(), &contractv1.HostnameRequest{Slug: "shop", Host: "app.acme.com"})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(remove); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveHostname() = %q, %v", result.GetError(), err)
	}
	if revoked := relay.RevokedOriginCertificates(); !slices.Equal(revoked, []string{"origin-certificate-1"}) {
		t.Errorf("the edge revoked %v, want origin-certificate-1: an origin certificate left valid outlives the hostname it answered", revoked)
	}
}

func TestAServedHostnameWhoseOriginCertificateIsDueIsIssuedASuccessorAndThePredecessorRevoked(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	relay.IssuesOriginCertificates()
	deployed(t, vendor, environment.TierProduction, "shop")
	addWebHostname(t, client, "app.acme.com", nil)

	relay.IssuesOriginCertificatesExpiringIn(time.Hour)
	relay.ForgetsOriginCertificate("app.acme.com")
	state := readStack(t, vendor, environment.TierProduction, "shop")
	hostState := state.Host("app.acme.com")
	hostState.OriginCertificateExpiresAt = time.Now().Add(time.Hour)
	state.SetHost("app.acme.com", hostState)
	seedStack(t, vendor, environment.TierProduction, "shop", state)

	addWebHostname(t, client, "app.acme.com", nil)
	if recorded := readStack(t, vendor, environment.TierProduction, "shop").Host("app.acme.com").OriginCertificateID; recorded != "origin-certificate-2" {
		t.Errorf("the hostname records origin certificate %q, want its successor origin-certificate-2", recorded)
	}
	if revoked := relay.RevokedOriginCertificates(); !slices.Equal(revoked, []string{"origin-certificate-1"}) {
		t.Errorf("the edge revoked %v, want the predecessor origin-certificate-1", revoked)
	}
}

func TestRemovingAProjectRevokesTheOriginCertificatesItsHostnamesWereAnsweredWith(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	relay.IssuesOriginCertificates()
	deployed(t, vendor, environment.TierProduction, "shop")
	addWebHostname(t, client, "app.acme.com", nil)

	stream, err := client.RemoveProject(context.Background(), projectRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveProject() = %q, %v", result.GetError(), err)
	}
	if revoked := relay.RevokedOriginCertificates(); !slices.Equal(revoked, []string{"origin-certificate-1"}) {
		t.Errorf("the edge revoked %v, want origin-certificate-1: nothing answers with it once the project is gone", revoked)
	}
}

func TestTheRemovalPlanNamesAClaimGivenBackAndTakenAgain(t *testing.T) {
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
	if result, err := drain(remove); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveHostname() = %q, %v", result.GetError(), err)
	}
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
		t.Errorf("the removal plan names claims %v, want app.acme.com, which the router holds again", claimed)
	}
}

func TestAnOriginCertificateIssuedForAClaimTheRouterRefusesIsRevoked(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	relay.IssuesOriginCertificates()
	relay.RefusesClaimsCarryingAnOriginCertificate(errors.New("the box could not load the certificate"))
	deployed(t, vendor, environment.TierProduction, "shop")

	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []*contractv1.ConfiguredHostname{{Hostname: "app.acme.com", App: "web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || result.GetSuccess() {
		t.Fatalf("AddHostname() succeeded = %t, %v, want it failed with the claim the router refused", result.GetSuccess(), err)
	}
	if revoked := relay.RevokedOriginCertificates(); !slices.Equal(revoked, []string{"origin-certificate-1"}) {
		t.Errorf("the edge revoked %v, want origin-certificate-1: a certificate nothing answers with stays valid for a year unless it is revoked", revoked)
	}
}

func addWebHostnameRefused(t *testing.T, client contractv1connect.ProviderServiceClient, host string) {
	t.Helper()
	stream, err := client.AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       "shop",
		Configured: []*contractv1.ConfiguredHostname{{Hostname: host, App: "web"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(stream); err != nil || result.GetSuccess() {
		t.Fatalf("AddHostname() succeeded = %t, %v, want it failed with the bind the edge refused", result.GetSuccess(), err)
	}
}

func TestAHostnameWhoseFirstBindIsRefusedGivesItsClaimBackAndRevokesTheOriginCertificateIssuedForIt(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	relay.IssuesOriginCertificates()
	relay.RefusesBinds(errors.New("a record of that name was not written by ocel"))
	deployed(t, vendor, environment.TierProduction, "shop")

	addWebHostnameRefused(t, client, "app.acme.com")

	if disclaimed := relay.Disclaimed(); !slices.Equal(disclaimed, []string{"app.acme.com"}) {
		t.Errorf("the router gave back %v, want app.acme.com: no edge forwards a hostname whose bind failed", disclaimed)
	}
	if revoked := relay.RevokedOriginCertificates(); !slices.Equal(revoked, []string{"origin-certificate-1"}) {
		t.Errorf("the edge revoked %v, want origin-certificate-1: nothing answers with the certificate of a claim given back", revoked)
	}
	if recorded := readStack(t, vendor, environment.TierProduction, "shop").Host("app.acme.com").OriginCertificateID; recorded != "" {
		t.Errorf("the hostname records origin certificate %q, want none: the one issued for it is revoked", recorded)
	}

	relay.RefusesBinds(nil)
	addWebHostname(t, client, "app.acme.com", nil)
	if recorded := readStack(t, vendor, environment.TierProduction, "shop").Host("app.acme.com").OriginCertificateID; recorded != "origin-certificate-2" {
		t.Errorf("the hostname records origin certificate %q, want origin-certificate-2, issued when it was claimed again: a certificate the hostname does not record is never renewed", recorded)
	}
}

func TestAServedHostnameWhoseRenewalBindIsRefusedRecordsTheSuccessorTheRouterHoldsAndRevokesThePredecessor(t *testing.T) {
	t.Parallel()
	client, vendor := contractServed(t, "1.0.0")
	relay := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	relay.IssuesOriginCertificates()
	deployed(t, vendor, environment.TierProduction, "shop")
	addWebHostname(t, client, "app.acme.com", nil)

	relay.ForgetsOriginCertificate("app.acme.com")
	state := readStack(t, vendor, environment.TierProduction, "shop")
	hostState := state.Host("app.acme.com")
	hostState.OriginCertificateExpiresAt = time.Now().Add(time.Hour)
	state.SetHost("app.acme.com", hostState)
	seedStack(t, vendor, environment.TierProduction, "shop", state)
	relay.RefusesBinds(errors.New("cloudflare answered 500"))

	addWebHostnameRefused(t, client, "app.acme.com")

	if recorded := readStack(t, vendor, environment.TierProduction, "shop").Host("app.acme.com").OriginCertificateID; recorded != "origin-certificate-2" {
		t.Errorf("the hostname records origin certificate %q, want origin-certificate-2, which the router answers it with: a certificate the hostname does not record is never renewed nor revoked", recorded)
	}
	if revoked := relay.RevokedOriginCertificates(); !slices.Equal(revoked, []string{"origin-certificate-1"}) {
		t.Errorf("the edge revoked %v, want the predecessor origin-certificate-1, which nothing answers with any more", revoked)
	}
	if disclaimed := relay.Disclaimed(); len(disclaimed) != 0 {
		t.Errorf("the router gave back %v, want nothing: the hostname is still served", disclaimed)
	}
}
