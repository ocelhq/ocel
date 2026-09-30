package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestAContainerAppsPreviewBehindAnEdgeRunningCodeIsForwardedUnderItsOwnHostAndGoneWithThePreview(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)

	result, _ := deploy(t, client, withContainerAdmin(previewRequest(), adminDeploymentID))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("preview Deploy() = %q", result.GetError())
	}
	host := servedAppHost(t, result, "admin")
	direct := p.Edges().(*fake.Edges).Edge(fake.KindDirect)
	claimed := slices.IndexFunc(direct.Claims(), func(claim router.Claim) bool { return claim.Hostname == host })
	if claimed < 0 || direct.Claims()[claimed].Pointer != "pr-7" || direct.Claims()[claimed].App != "admin" {
		t.Fatalf("the %s router took claims %+v, want %s claimed for admin on pr-7", fake.RouterDirect, direct.Claims(), host)
	}
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	bound := slices.IndexFunc(relay.Bindings(), func(binding edge.DomainBinding) bool { return binding.Hostname == host })
	if bound < 0 || relay.Bindings()[bound].Origin == nil {
		t.Fatalf("the edge was bound with %+v, want %s forwarded to its router's origin", relay.Bindings(), host)
	}
	served := p.Routers().(*fake.Routers).DataPlane(fake.RouterDirect).Builds("shop", environment.TierPreview, "pr-7")
	if served["admin"] == "" {
		t.Errorf("the %s router serves %v on pr-7, want admin's build", fake.RouterDirect, served)
	}

	stream, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT},
	})
	if err != nil {
		t.Fatalf("RemoveEnvironment() error = %v", err)
	}
	if removed, err := drain(stream); err != nil || !removed.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, %v", removed.GetError(), err)
	}
	state := readStack(t, p, environment.TierPreview, "shop")
	if slices.Contains(state.Edge.Bound, host) {
		t.Errorf("the edge still binds %v after pr-7 was removed, want %s unbound with it", state.Edge.Bound, host)
	}
	if _, kept := state.Hosts[host]; kept {
		t.Errorf("the edge state still records %s after pr-7 was removed", host)
	}
	if left := p.Routers().(*fake.Routers).DataPlane(fake.RouterDirect).Builds("shop", environment.TierPreview, "pr-7"); len(left) != 0 {
		t.Errorf("the %s router still serves %v on pr-7 after it was removed", fake.RouterDirect, left)
	}
}

func TestAContainerAppsPreviewBehindAnEdgeRunningCodePublishesNoDeploymentURLTheEdgeDoesNotForward(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)

	result, _ := deploy(t, client, withContainerAdmin(previewRequest(), adminDeploymentID))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("preview Deploy() = %q", result.GetError())
	}
	if findDeploymentURL(result, "web") == "" {
		t.Errorf("web, served by the edge's own router, published no deployment URL")
	}
	url := findDeploymentURL(result, "admin")
	if url == "" {
		return
	}
	host := strings.TrimPrefix(url, "https://")
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	if !slices.ContainsFunc(relay.Bindings(), func(binding edge.DomainBinding) bool { return binding.Hostname == host && binding.Origin != nil }) {
		t.Errorf("admin's deployment URL is %s, which the edge does not forward to admin's origin: bindings %+v", url, relay.Bindings())
	}
}

func TestAContainerAppsPreviewIsClaimedAgainWithTheWildcardsRenewedCertificateBeforeTheOldOneIsDiscarded(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)
	p.RequireValidationRecords(edge.Record{Name: "_acme.preview.example", Type: edge.RecordTypeCNAME, Value: "validation.fake.invalid"})
	previewed := func() *contractv1.DeployRequest {
		req := withContainerAdmin(previewRequest(), adminDeploymentID)
		req.Edge = zoned("preview.example")
		return req
	}

	deployed, _ := deploy(t, client, previewed())
	if !deployed.GetSuccess() {
		t.Fatalf("preview Deploy() = %q", deployed.GetError())
	}
	host := servedAppHost(t, deployed, "admin")
	direct := p.Edges().(*fake.Edges).Edge(fake.KindDirect)
	first := lastClaimOf(direct.Claims(), host)
	if first.Certificate == "" || !first.CertificateRequested {
		t.Fatalf("%s was claimed with %+v, want the certificate ocel requested for the project's preview wildcard", host, first)
	}

	p.RotateCertificates()
	if result, _ := deploy(t, client, previewed()); !result.GetSuccess() {
		t.Fatalf("preview Deploy() after the wildcard's certificate was renewed = %q", result.GetError())
	}
	again := lastClaimOf(direct.Claims(), host)
	if again.Certificate == first.Certificate {
		t.Errorf("%s is still claimed with %s after the wildcard's certificate was renewed, want the renewed one: the router answers it with the certificate it was last claimed with", host, first.Certificate)
	}
	if !slices.Contains(p.Discarded(), first.Certificate) {
		t.Errorf("discarded %v, want %s, the certificate no preview answers with once each was claimed again", p.Discarded(), first.Certificate)
	}
}

func lastClaimOf(claims []router.Claim, hostname string) router.Claim {
	for i := len(claims) - 1; i >= 0; i-- {
		if claims[i].Hostname == hostname {
			return claims[i]
		}
	}
	return router.Claim{}
}

func servedAppHost(t *testing.T, result *progressv1.OperationResult, app string) string {
	t.Helper()
	urls := servedAppURLs(result, app)
	if len(urls) == 0 {
		t.Fatalf("the deploy served %s on no URL", app)
	}
	return strings.TrimPrefix(urls[0], "https://")
}
