package providerserver_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

type frontedByCompute struct{ *fake.Provider }

func (p frontedByCompute) Facts() provider.Facts {
	facts := p.Provider.Facts()
	facts.Pairings = []provider.Pairing{
		{Edge: fake.KindRelay, Router: fake.RouterRelay, Computes: []provider.Compute{provider.ComputeServerless}},
		{Edge: fake.KindRelay, Router: fake.RouterDirect, Computes: []provider.Compute{provider.ComputeContainer}, Forwarded: true, FrontedBy: fake.RouterRelay},
		{Edge: fake.KindDirect, Router: fake.RouterDirect, Computes: provider.Computes()},
	}
	return facts
}

func frontedServed(t *testing.T, fronts bool) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	if fronts {
		relay.FrontsOrigins()
	}
	return servedBy(t, frontedByCompute{p}), p
}

func releasesOf(result *progressv1.OperationResult) map[string]string {
	releases := map[string]string{}
	for _, app := range result.GetApps() {
		releases[app.GetApp()] = app.GetRelease()
	}
	return releases
}

func TestAPromoteThroughAnEdgeThatFrontsItsOriginsMovesAFrontedAppOnItsOwnRouterAndOnTheOneFrontingIt(t *testing.T) {
	client, p := frontedServed(t, true)

	result, _ := deploy(t, client, mixedRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	planes := p.Routers().(*fake.Routers)
	relayed := planes.DataPlane(fake.RouterRelay).Releases("shop", environment.TierProduction, router.DefaultPointer)
	direct := planes.DataPlane(fake.RouterDirect).Releases("shop", environment.TierProduction, router.DefaultPointer)
	if want := releasesOf(result); !maps.Equal(relayed, want) {
		t.Errorf("the %s router serves %v, want %v: the edge's own router holds the fronted container's records beside web's", fake.RouterRelay, relayed, want)
	}
	if direct["admin"] == "" || direct["web"] != "" {
		t.Errorf("the %s router serves %v, want admin alone: admin still reaches through its own router", fake.RouterDirect, direct)
	}
	records := planes.DataPlane(fake.RouterRelay).Records("shop", environment.TierProduction, router.DefaultPointer)
	if !records["admin"].Fronted || records["web"].Fronted {
		t.Errorf("the %s router holds admin fronted %v and web fronted %v, want admin alone marked fronted: the edge reaches it through the hostname it was asked for", fake.RouterRelay, records["admin"].Fronted, records["web"].Fronted)
	}
}

func TestAPromoteThroughAnEdgeThatFrontsNoOriginLeavesAForwardedAppToItsOwnRouter(t *testing.T) {
	client, p := frontedServed(t, false)

	result, _ := deploy(t, client, mixedRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	relayed := p.Routers().(*fake.Routers).DataPlane(fake.RouterRelay).Releases("shop", environment.TierProduction, router.DefaultPointer)
	if relayed["admin"] != "" {
		t.Errorf("the %s router serves %v, want no admin: an edge that fronts no origin forwards admin past its code", fake.RouterRelay, relayed)
	}
	stacks := p.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks()
	if fronted := stacks[len(stacks)-1].Fronted; len(fronted) != 0 {
		t.Errorf("the edge reconciled fronted hosts %v, want none", fronted)
	}
}

func TestARollbackMovesAFrontedAppBackOnTheRouterFrontingItToo(t *testing.T) {
	client, p := frontedServed(t, true)
	first, _ := deploy(t, client, mixedRequest())
	if first == nil || !first.GetSuccess() {
		t.Fatalf("Deploy() = %q", first.GetError())
	}
	second, _ := deploy(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))
	if second == nil || !second.GetSuccess() {
		t.Fatalf("the second Deploy() = %q", second.GetError())
	}

	if _, err := rollBack(context.Background(), client, &contractv1.RollbackRequest{Slug: "shop"}); err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}

	relayed := p.Routers().(*fake.Routers).DataPlane(fake.RouterRelay).Releases("shop", environment.TierProduction, router.DefaultPointer)
	if want := releasesOf(first); !maps.Equal(relayed, want) {
		t.Errorf("after the rollback the %s router serves %v, want the first deploy's %v", fake.RouterRelay, relayed, want)
	}
}

func TestADeployHandsAnEdgeThatFrontsItsOriginsEachHostOfAFrontedAppAndTheAppItServes(t *testing.T) {
	client, p := frontedServed(t, true)

	result, _ := deploy(t, client, mixedRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	stacks := p.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks()
	last := stacks[len(stacks)-1]
	if !slices.Equal(last.Fronted, []string{adminHost}) {
		t.Errorf("the edge reconciled fronted hosts %v, want [%s]", last.Fronted, adminHost)
	}
	if slices.Contains(last.Domains, adminHost) {
		t.Errorf("the edge reconciled domains %v, want no %s: a fronted host stays forwarded to its own router", last.Domains, adminHost)
	}
	if last.DomainApps[adminHost] != "admin" {
		t.Errorf("the edge reconciled domain apps %v, want %s serving admin: its code reads the records of the app a host serves", last.DomainApps, adminHost)
	}
}

func TestAFrontedContainerAppsNeedsAreCheckedAgainstTheRouterFrontingIt(t *testing.T) {
	client, p := frontedServed(t, true)
	p.Edges().(*fake.Edges).Edge(fake.KindDirect).RouterServes([]edge.Need{edge.NeedStreaming})
	declaresNeed(t, "admin", edge.NeedEdgeMiddleware)

	result, _ := deploy(t, client, mixedRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want admin's %s served by the edge's code fronting it", result.GetError(), edge.NeedEdgeMiddleware)
	}
}

func TestAContainerAppForwardedPastAnEdgeThatFrontsNoOriginIsStillRefusedWhatOnlyItsCodeServes(t *testing.T) {
	client, p := frontedServed(t, false)
	p.Edges().(*fake.Edges).Edge(fake.KindDirect).RouterServes([]edge.Need{edge.NeedStreaming})
	declaresNeed(t, "admin", edge.NeedEdgeMiddleware)

	result, _ := deploy(t, client, mixedRequest())
	if result.GetSuccess() || !strings.Contains(result.GetError(), "app admin needs "+string(edge.NeedEdgeMiddleware)) {
		t.Fatalf("Deploy() = %q, want admin refused for %s", result.GetError(), edge.NeedEdgeMiddleware)
	}
}

type frontedOnly struct{ *fake.Provider }

func (p frontedOnly) Facts() provider.Facts {
	facts := p.Provider.Facts()
	facts.Pairings = []provider.Pairing{
		{Edge: fake.KindRelay, Router: fake.RouterDirect, Computes: provider.Computes(), FrontedBy: fake.RouterRelay},
		{Edge: fake.KindDirect, Router: fake.RouterDirect, Computes: provider.Computes()},
	}
	return facts
}

func TestAnEdgeThatFrontsTheOnlyRouterItPairsWithRoutesItselfThroughTheRouterFrontingIt(t *testing.T) {
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	relay.FrontsOrigins()
	client := servedBy(t, frontedOnly{p})

	result, _ := deploy(t, client, mixedRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	planes := p.Routers().(*fake.Routers)
	want := releasesOf(result)
	if got := planes.DataPlane(fake.RouterRelay).Releases("shop", environment.TierProduction, router.DefaultPointer); !maps.Equal(got, want) {
		t.Errorf("the %s router serves %v, want %v: the edge's own records hold every app it fronts", fake.RouterRelay, got, want)
	}
	if got := planes.DataPlane(fake.RouterDirect).Releases("shop", environment.TierProduction, router.DefaultPointer); !maps.Equal(got, want) {
		t.Errorf("the %s router serves %v, want %v: each app still reaches through it", fake.RouterDirect, got, want)
	}
	stacks := p.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks()
	if domains := stacks[len(stacks)-1].Domains; slices.Contains(domains, adminHost) {
		t.Errorf("the edge reconciled domains %v, want no %s: a fronted host stays forwarded to its own router", domains, adminHost)
	}
}

func TestAPreviewOnTheSharedWildcardHandsAnEdgeThatFrontsItsOriginsTheHostOfItsFrontedApp(t *testing.T) {
	client, p := frontedServed(t, true)
	previewBootstrapped(t, client)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}
	p.RequireValidationRecords(globalWildcardValidation)

	result, _ := deploy(t, client, globalPreviewOf("shop"))
	if result == nil || !result.GetSuccess() {
		t.Fatalf("preview Deploy() = %q", result.GetError())
	}

	stacks := p.Edges().(*fake.Edges).Edge(fake.KindRelay).Stacks()
	last := stacks[len(stacks)-1]
	if host := servedAppHost(t, result, "admin"); !last.PruneOnly || !slices.Equal(last.Fronted, []string{host}) {
		t.Errorf("the edge reconciled prune-only %v with fronted hosts %v, want [%s]: the shared preview entry fronts admin's preview", last.PruneOnly, last.Fronted, host)
	}
}

func frontedOnlyServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider, *fake.Edge) {
	t.Helper()
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	relay.ProxiesRecords()
	return servedBy(t, frontedOnly{p}), p, relay
}

func countBindings(relay *fake.Edge, hostname string) int {
	n := 0
	for _, binding := range relay.Bindings() {
		if binding.Hostname == hostname {
			n++
		}
	}
	return n
}

func TestAnEdgeThatStartsFrontingTheRouterItRoutedThroughTakesTheProjectOverAndBindsItsHostsAgain(t *testing.T) {
	client, p, relay := frontedOnlyServed(t)
	if first, _ := deploy(t, client, mixedRequest()); first == nil || !first.GetSuccess() {
		t.Fatalf("Deploy() = %q", first.GetError())
	}
	bound := countBindings(relay, adminHost)
	relay.FrontsOrigins()

	second, _ := deploy(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))
	if second == nil || !second.GetSuccess() {
		t.Fatalf("the Deploy() after the edge fronts its origins = %q, want the project taken over", second.GetError())
	}

	planes := p.Routers().(*fake.Routers)
	want := releasesOf(second)
	for _, kind := range []router.Kind{fake.RouterRelay, fake.RouterDirect} {
		if got := planes.DataPlane(kind).Releases("shop", environment.TierProduction, router.DefaultPointer); !maps.Equal(got, want) {
			t.Errorf("the %s router serves %v, want %v", kind, got, want)
		}
	}
	if again := countBindings(relay, adminHost); again <= bound {
		t.Errorf("%s was bound %d times, and %d before the edge fronted it, want it bound again so the edge's code fronts it", adminHost, again, bound)
	}
}

func TestAnEdgeThatStopsFrontingItsOriginsHandsTheProjectBackToItsRouterAndBindsItsHostsAgain(t *testing.T) {
	client, p, relay := frontedOnlyServed(t)
	relay.FrontsOrigins()
	if first, _ := deploy(t, client, mixedRequest()); first == nil || !first.GetSuccess() {
		t.Fatalf("Deploy() = %q", first.GetError())
	}
	bound := countBindings(relay, adminHost)
	relay.FrontsNoOrigin()

	second, _ := deploy(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))
	if second == nil || !second.GetSuccess() {
		t.Fatalf("the Deploy() after the edge stops fronting = %q", second.GetError())
	}

	got := p.Routers().(*fake.Routers).DataPlane(fake.RouterDirect).Releases("shop", environment.TierProduction, router.DefaultPointer)
	if want := releasesOf(second); !maps.Equal(got, want) {
		t.Errorf("the %s router serves %v, want %v", fake.RouterDirect, got, want)
	}
	if again := countBindings(relay, adminHost); again <= bound {
		t.Errorf("%s was bound %d times, and %d while the edge fronted it, want it bound again with no worker in front", adminHost, again, bound)
	}
}
