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
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestAContainerAppsPreviewBehindAnEdgeRunningCodeIsForwardedUnderItsOwnHostAndGoneWithThePreview(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)

	result, _ := deploy(t, client, withContainerAdmin(previewRequest(), adminBuildID))
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
	served := p.Routers().(*fake.Routers).DataPlane(fake.RouterDirect).Releases("shop", environment.TierPreview, "pr-7")
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
	if left := p.Routers().(*fake.Routers).DataPlane(fake.RouterDirect).Releases("shop", environment.TierPreview, "pr-7"); len(left) != 0 {
		t.Errorf("the %s router still serves %v on pr-7 after it was removed", fake.RouterDirect, left)
	}
}

func TestAForwardedPreviewDeploymentIsPublishedOnItsOwnHostname(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)

	result, _ := deploy(t, client, withContainerAdmin(previewRequest(), adminBuildID))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("preview Deploy() = %q", result.GetError())
	}

	url := findDeploymentURL(result, "admin")
	if url == "" {
		t.Fatal("admin, forwarded through its router, published no deployment URL")
	}
	host := strings.TrimPrefix(url, "https://")
	if alias := servedAppHost(t, result, "admin"); alias == host {
		t.Fatalf("admin's deployment URL %s is its alias: each deployment answers on a hostname of its own", url)
	}
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	if !slices.ContainsFunc(relay.Bindings(), func(binding edge.DomainBinding) bool { return binding.Hostname == host && binding.Origin != nil }) {
		t.Errorf("the edge was bound with %+v, want %s forwarded to admin's origin", relay.Bindings(), host)
	}
	direct := p.Edges().(*fake.Edges).Edge(fake.KindDirect)
	claim := lastClaimOf(direct.Claims(), host)
	if want := router.FormatDeploymentPointer("pr-7", result.GetPromotionId()); claim.Pointer != want || claim.App != "admin" {
		t.Errorf("%s was claimed as %+v, want admin on the deployment pointer %s", host, claim, want)
	}
}

func TestASupersededForwardedDeploymentHostnameIsUnboundAndDisclaimed(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)
	var results []*progressv1.OperationResult
	for range 3 {
		result, _ := deploy(t, client, withContainerAdmin(previewRequest(), adminBuildID))
		if result == nil || !result.GetSuccess() {
			t.Fatalf("preview Deploy() = %q", result.GetError())
		}
		results = append(results, result)
	}
	hostOf := func(result *progressv1.OperationResult) string {
		return strings.TrimPrefix(findDeploymentURL(result, "admin"), "https://")
	}
	first, last, alias := hostOf(results[0]), hostOf(results[2]), servedAppHost(t, results[2], "admin")

	pruning, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug: "shop", KeepN: 1, Environment: previewRequest().GetEnvironment(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if pruned, err := drain(pruning); err != nil || !pruned.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", pruned.GetError(), err)
	}

	state := readStack(t, p, environment.TierPreview, "shop")
	if slices.Contains(state.Edge.Bound, first) {
		t.Errorf("the edge still binds %s after its deployment was pruned: %v", first, state.Edge.Bound)
	}
	if _, kept := state.Hosts[first]; kept {
		t.Errorf("the edge state still records %s after its deployment was pruned", first)
	}
	if !slices.Contains(p.Edges().(*fake.Edges).Edge(fake.KindDirect).Disclaimed(), first) {
		t.Errorf("the router never disclaimed %s: its trust claim would outlive the deployment", first)
	}
	for _, kept := range []string{last, alias} {
		if !slices.Contains(state.Edge.Bound, kept) {
			t.Errorf("%s is no longer bound after the prune, want what the prune kept still forwarded: %v", kept, state.Edge.Bound)
		}
	}
}

func TestAnEdgeRoutedPreviewDeploymentIsPublishedAsBefore(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)

	result, _ := deploy(t, client, withContainerAdmin(previewRequest(), adminBuildID))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("preview Deploy() = %q", result.GetError())
	}

	url := findDeploymentURL(result, "web")
	if url == "" {
		t.Fatal("web, served by the edge's own router, published no deployment URL")
	}
	direct := p.Edges().(*fake.Edges).Edge(fake.KindDirect)
	if claim := lastClaimOf(direct.Claims(), strings.TrimPrefix(url, "https://")); claim.Hostname != "" {
		t.Errorf("web's deployment hostname was claimed on the forwarded router: %+v", claim)
	}
}

func TestAContainerAppsPreviewIsClaimedAgainWithTheWildcardsRenewedCertificateBeforeTheOldOneIsDiscarded(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)
	p.RequireValidationRecords(edge.Record{Name: "_acme.preview.example", Type: edge.RecordTypeCNAME, Value: "validation.fake.invalid"})
	previewed := func() *contractv1.DeployRequest {
		req := withContainerAdmin(previewRequest(), adminBuildID)
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

func TestRemovingOneProjectUnderTheGlobalPreviewWildcardLeavesTheCertificateAnotherProjectsContainerPreviewsAnswerWith(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}
	validation := edge.Record{Name: "_acme.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "validation.fake.invalid"}
	p.RequireValidationRecords(validation)
	previewOf := func(slug string) *contractv1.DeployRequest {
		req := withContainerAdmin(previewDeployRequest(), adminBuildID)
		req.Manifest.Slug = slug
		req.Edge = edged(fake.KindRelay, "acme.com")
		return req
	}
	if result, _ := deploy(t, client, previewOf("shop")); !result.GetSuccess() {
		t.Fatalf("shop's preview Deploy() = %q", result.GetError())
	}
	blogged, _ := deploy(t, client, previewOf("blog"))
	if !blogged.GetSuccess() {
		t.Fatalf("blog's preview Deploy() = %q", blogged.GetError())
	}
	blogHost := servedAppHost(t, blogged, "admin")
	direct := p.Edges().(*fake.Edges).Edge(fake.KindDirect)
	shared := lastClaimOf(direct.Claims(), blogHost).Certificate
	if shared == "" {
		t.Fatalf("blog's %s was claimed with no certificate, want the global preview wildcard's", blogHost)
	}

	if removed := removeProject(t, client, &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
		Edge:        edged(fake.KindRelay, "acme.com"),
	}); !removed.GetSuccess() {
		t.Fatalf("RemoveProject() of shop = %q", removed.GetError())
	}

	if slices.Contains(p.Discarded(), shared) {
		t.Errorf("removing shop discarded %s, the certificate blog's previews still answer with", shared)
	}
	if slices.Contains(direct.Disclaimed(), blogHost) {
		t.Errorf("removing shop disclaimed blog's %s", blogHost)
	}
	if held := readStack(t, p, environment.TierPreview, "blog").Hosts[blogHost]; held.Certificate.ID != shared {
		t.Errorf("blog records %s answered with %q after shop was removed, want %s", blogHost, held.Certificate.ID, shared)
	}
	zone, err := p.DNS().Open(fake.KindZone, "acme.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(zone.(*fake.DNSRecords).Records(), validation) {
		t.Errorf("the zone holds %v after shop was removed, want %s kept: the shared certificate is renewed through it", zone.(*fake.DNSRecords).Records(), validation)
	}
}

func TestRemovingTheOnlyProjectWhoseGlobalPreviewWildcardCertificateIsStillValidatingDiscardsIt(t *testing.T) {
	client, p := mixedServed(t)
	previewBootstrapped(t, client)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}
	p.RequireValidationRecords(edge.Record{Name: "_acme.preview.acme.com", Type: edge.RecordTypeCNAME, Value: "validation.fake.invalid"})
	p.StallAfterProving(provider.Resumable(refusal.Refuse(refusal.CodeNotReady, "the certificate is still validating")))
	req := withContainerAdmin(previewDeployRequest(), adminBuildID)
	req.Edge = edged(fake.KindRelay, "acme.com")
	deploy(t, client, req)
	pending := readStack(t, p, environment.TierPreview, "shop").Hosts["*.preview.acme.com"].Certificate
	if pending.ID == "" || !pending.Requested {
		t.Fatalf("shop records the wildcard's certificate as %+v, want the one it requested while it validates", pending)
	}

	if removed := removeProject(t, client, &contractv1.ProjectRequest{
		Slug:        "shop",
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW},
		Edge:        edged(fake.KindRelay, "acme.com"),
	}); !removed.GetSuccess() {
		t.Fatalf("RemoveProject() of shop = %q", removed.GetError())
	}
	if !slices.Contains(p.Discarded(), pending.ID) {
		t.Errorf("removing shop discarded %v, want %s: no other project can answer with a certificate still validating", p.Discarded(), pending.ID)
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
