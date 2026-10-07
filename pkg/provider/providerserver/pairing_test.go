package providerserver_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestADeployRecordsTheRouterEachAppPromotesThrough(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	state := readStack(t, p, environment.TierProduction, "shop")
	relay := fake.RouterRelay
	if state.Apps["web"] != relay {
		t.Errorf("the edge state pairs web with %q, want %q: a later rollback or removal reads which router an app promotes through from here", state.Apps["web"], relay)
	}
	recorded, found := state.Routers[relay]
	if !found || recorded.Slug != "shop" || recorded.Tier != environment.TierProduction {
		t.Errorf("the edge state records routers %v, want the %q router of shop in production", state.Routers, relay)
	}
	if !recorded.Edge.Empty() {
		t.Errorf("the %q router's recorded state repeats the edge state %+v; the router shares the edge's stack, so its state is read from the edge's", relay, recorded.Edge)
	}
}

type unfronted struct{ *fake.Provider }

func (p unfronted) Facts() provider.Facts {
	facts := p.Provider.Facts()
	facts.DefaultEdge = edge.None
	facts.Pairings = append(facts.Pairings, provider.Pairing{Edge: edge.None, Router: fake.RouterDirect, Computes: provider.Computes()})
	return facts
}

func (p unfronted) Bootstrap(kind edge.Kind) (provider.Bootstrap, error) {
	if kind == edge.None {
		kind = fake.KindDirect
	}
	return p.Provider.Bootstrap(kind)
}

func (p unfronted) Edges() provider.Edges { return originEdges{p.Provider.Edges()} }

type originEdges struct{ provider.Edges }

func (e originEdges) Open(kind edge.Kind, options provider.Options) (edge.Edge, error) {
	if kind != edge.None {
		return e.Edges.Open(kind, options)
	}
	front, err := e.Edges.Open(fake.KindDirect, options)
	return origin{front}, err
}

type origin struct{ edge.Edge }

func (origin) Kind() edge.Kind { return edge.None }

func TestAProjectWithNoEdgeIsServedByTheRouterNoEdgePairsWithAndRemovedThroughNoEdge(t *testing.T) {
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, unfronted{p})
	bootstrappedOverRPC(t, client)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() with no edge = %q", result.GetError())
	}
	if !slices.Equal(servedURLs(result), []string{"https://shop.example"}) {
		t.Errorf("the deploy served %v, want shop.example answered once the router no edge pairs with answers it", servedURLs(result))
	}
	state := readStack(t, p, environment.TierProduction, "shop")
	if state.Kind != edge.None || state.Apps["web"] != fake.RouterDirect {
		t.Errorf("the edge state records edge %q and pairs web with %q, want no edge and the %s router", state.Kind, state.Apps["web"], fake.RouterDirect)
	}
	if host := state.Host("shop.example"); host.Edge != edge.None || !host.Probe.OK || host.Probe.Router != fake.RouterDirect {
		t.Errorf("shop.example is recorded %+v, want it bound to no edge and answered by the %s router", host, fake.RouterDirect)
	}

	removal := projectRequest()
	removal.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}
	plan, err := client.PlanRemoveProject(context.Background(), removal)
	if err != nil {
		t.Fatalf("PlanRemoveProject() = %v", err)
	}
	if plan.GetEdgeKind() != string(edge.None) {
		t.Errorf("the removal plan is fronted by %q, want no edge: the project was deployed with none, and a config that now names another does not change what serves it", plan.GetEdgeKind())
	}
}

func TestEachAppStackIsHandedTheRouterItsAppPromotesThroughAndNotItsEdge(t *testing.T) {
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, unfronted{p})
	bootstrappedOverRPC(t, client)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() with no edge = %q", result.GetError())
	}
	var routers []router.Kind
	for _, spec := range p.FakeStacks().Provisioned() {
		if spec.App != nil && spec.App.App == "web" {
			routers = append(routers, spec.App.Router)
		}
	}
	if !slices.Equal(routers, []router.Kind{fake.RouterDirect}) {
		t.Errorf("web's stack was handed routers %v, want [%s]: the router, not the edge, names itself on what the app answers", routers, fake.RouterDirect)
	}
}

type pairedForContainersOnly struct{ *fake.Provider }

func (p pairedForContainersOnly) Facts() provider.Facts {
	facts := p.Provider.Facts()
	for i := range facts.Pairings {
		facts.Pairings[i].Computes = []provider.Compute{provider.ComputeContainer}
	}
	return facts
}

func TestAnAppWhoseComputeNoRouterOfItsEdgePairsWithIsRefusedBeforeAnythingIsDeployed(t *testing.T) {
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, pairedForContainersOnly{p})
	bootstrappedOverRPC(t, client)

	result, _, err := deployStream(t, client, deployRequest())
	message := result.GetError()
	if err != nil {
		message = err.Error()
	}
	if result.GetSuccess() || !strings.Contains(message, "app web runs as serverless") {
		t.Fatalf("Deploy() of a serverless app on a provider that pairs its edge for containers only = %q, want it refused naming web: no router of the edge moves its pointer", message)
	}
	if routed := p.Routers().(*fake.Routers).DataPlane(fake.RouterRelay).Releases("shop", environment.TierProduction, router.DefaultPointer); len(routed) != 0 {
		t.Errorf("the relay edge routes %v after the refused deploy, want nothing", routed)
	}
}

const adminHost = "admin.shop.example"

type pairedByCompute struct{ *fake.Provider }

func (p pairedByCompute) Facts() provider.Facts {
	facts := p.Provider.Facts()
	facts.Pairings = []provider.Pairing{
		{Edge: fake.KindRelay, Router: fake.RouterRelay, Computes: []provider.Compute{provider.ComputeServerless}},
		{Edge: fake.KindRelay, Router: fake.RouterDirect, Computes: []provider.Compute{provider.ComputeContainer}, Forwarded: true},
		{Edge: fake.KindDirect, Router: fake.RouterDirect, Computes: provider.Computes()},
	}
	return facts
}

func mixedServed(t *testing.T) (contractv1connect.ProviderServiceClient, *fake.Provider) {
	t.Helper()
	daemonWithTheBuiltImage(t, "amd64")
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	p.Edges().(*fake.Edges).Edge(fake.KindRelay).ProxiesRecords()
	client := servedBy(t, pairedByCompute{p})
	return client, p
}

func withContainerAdmin(req *contractv1.DeployRequest, buildID string) *contractv1.DeployRequest {
	req = namingARegistry(req)
	admin := &contractv1.ManifestApp{
		Name:      "admin",
		Framework: &contractv1.Framework{Name: "next"},
		BuildId:   buildID,
		Artifact: &contractv1.ManifestApp_Container{Container: &contractv1.ContainerArtifact{
			Image:           containerTestImage,
			HealthCheckPath: "/",
			MinInstances:    1,
			MaxInstances:    1,
		}},
	}
	if req.GetEnvironment().GetTier() == environmentv1.Tier_TIER_PRODUCTION {
		admin.Domains = []*contractv1.TierDomains{{Tier: environmentv1.Tier_TIER_PRODUCTION, Hostnames: []string{adminHost}}}
	}
	req.Manifest.Apps = append(req.Manifest.Apps, admin)
	return req
}

func mixedRequest() *contractv1.DeployRequest {
	return withContainerAdmin(deployRequest(), adminBuildID)
}

func TestAProjectMixingServerlessAndContainerAppsPromotesEachThroughTheRouterItsComputePairsWith(t *testing.T) {
	client, p := mixedServed(t)

	first, _ := deploy(t, client, mixedRequest())
	if first == nil || !first.GetSuccess() {
		t.Fatalf("Deploy() of a project mixing serverless and container apps = %q, want it served through one edge", first.GetError())
	}
	planes := p.Routers().(*fake.Routers)
	relayed := planes.DataPlane(fake.RouterRelay).Releases("shop", environment.TierProduction, router.DefaultPointer)
	direct := planes.DataPlane(fake.RouterDirect).Releases("shop", environment.TierProduction, router.DefaultPointer)
	if relayed["web"] == "" || relayed["admin"] != "" {
		t.Errorf("the %s router serves %v, want web alone: admin runs as a container, which its edge pairs with %s", fake.RouterRelay, relayed, fake.RouterDirect)
	}
	if direct["admin"] == "" || direct["web"] != "" {
		t.Errorf("the %s router serves %v, want admin alone", fake.RouterDirect, direct)
	}

	state := readStack(t, p, environment.TierProduction, "shop")
	if state.Apps["web"] != fake.RouterRelay || state.Apps["admin"] != fake.RouterDirect {
		t.Errorf("the edge state pairs %v, want web with %s and admin with %s", state.Apps, fake.RouterRelay, fake.RouterDirect)
	}
	if own := state.Routers[fake.RouterDirect]; own.Slug != "shop" || own.Tier != environment.TierProduction {
		t.Errorf("the edge state records the %s router as %+v, want its own stack of shop in production: it keeps no state inside the edge's", fake.RouterDirect, own)
	}

	second, _ := deploy(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))
	if second == nil || !second.GetSuccess() {
		t.Fatalf("the second Deploy() = %q", second.GetError())
	}
	moved := planes.DataPlane(fake.RouterDirect).Releases("shop", environment.TierProduction, router.DefaultPointer)
	if moved["admin"] == direct["admin"] {
		t.Errorf("the %s router still serves admin's build %q after a promote of a new one: a promote flips every router its apps pair with", fake.RouterDirect, moved["admin"])
	}
	if planes.DataPlane(fake.RouterRelay).Releases("shop", environment.TierProduction, router.DefaultPointer)["web"] == "" {
		t.Errorf("the %s router serves no build of web after the second promote", fake.RouterRelay)
	}
}

func TestAContainerAppsHostnameIsClaimedOnItsOwnRouterAndForwardedByTheEdgeToItsOrigin(t *testing.T) {
	client, p := mixedServed(t)

	result, _ := deploy(t, client, mixedRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	direct := p.Edges().(*fake.Edges).Edge(fake.KindDirect)
	if !slices.ContainsFunc(direct.Claims(), func(claim router.Claim) bool { return claim.Hostname == adminHost && claim.App == "admin" }) {
		t.Errorf("the %s router took claims %+v, want %s for admin", fake.RouterDirect, direct.Claims(), adminHost)
	}
	relay := p.Edges().(*fake.Edges).Edge(fake.KindRelay)
	bound := slices.IndexFunc(relay.Bindings(), func(binding edge.DomainBinding) bool { return binding.Hostname == adminHost })
	if bound < 0 || relay.Bindings()[bound].Origin == nil || *relay.Bindings()[bound].Origin != fake.Origin(fake.RouterDirect) {
		t.Errorf("the edge was bound with %+v, want %s forwarded to %+v, the origin its router answers on", relay.Bindings(), adminHost, fake.Origin(fake.RouterDirect))
	}
	stacks := relay.Stacks()
	if domains := stacks[len(stacks)-1].Domains; slices.Contains(domains, adminHost) || !slices.Contains(domains, "shop.example") {
		t.Errorf("the edge reconciled domains %v, want shop.example and not %s: the edge runs its code on the hostnames its own router answers, and forwards the rest", domains, adminHost)
	}
	if host := readStack(t, p, environment.TierProduction, "shop").Host(adminHost); !host.Probe.OK || host.Probe.Router != fake.RouterDirect {
		t.Errorf("%s is recorded %+v, want it answered by %s", adminHost, host, fake.RouterDirect)
	}
	if !slices.Contains(servedURLs(result), "https://"+adminHost) {
		t.Errorf("the deploy served %v, want https://%s among them", servedURLs(result), adminHost)
	}
}

func TestADeployThatMovesAContainerAppsHostnameToAnotherContainerAppClaimsItForThatApp(t *testing.T) {
	client, p := mixedServed(t)

	first, _ := deploy(t, client, mixedRequest())
	if first == nil || !first.GetSuccess() {
		t.Fatalf("Deploy() = %q", first.GetError())
	}

	moved := mixedRequest()
	admin := moved.Manifest.Apps[len(moved.Manifest.Apps)-1]
	moved.Manifest.Apps = append(moved.Manifest.Apps, &contractv1.ManifestApp{
		Name:      "ops",
		Framework: admin.Framework,
		BuildId:   "0123456789abcdef0123456789abcdef",
		Artifact:  admin.Artifact,
		Domains:   admin.Domains,
	})
	admin.Domains = nil
	second, _ := deploy(t, client, moved)
	if second == nil || !second.GetSuccess() {
		t.Fatalf("the second Deploy() = %q", second.GetError())
	}

	direct := p.Edges().(*fake.Edges).Edge(fake.KindDirect)
	last := lastClaimOf(direct.Claims(), adminHost)
	if last.App != "ops" {
		t.Errorf("the %s router last took %+v for %s, want it claimed for ops: the router forwards a hostname to the app its claim names, so it serves admin until it is told otherwise", fake.RouterDirect, last, adminHost)
	}
}

func TestAContainerAppsNeedsAreCheckedAgainstTheRouterItPromotesThrough(t *testing.T) {
	client, p := mixedServed(t)
	p.Edges().(*fake.Edges).Edge(fake.KindDirect).RouterServes([]edge.Need{edge.NeedStreaming})
	declaresNeed(t, "web", edge.NeedEdgeMiddleware)
	declaresNeed(t, "admin", edge.NeedEdgeMiddleware)

	result, _ := deploy(t, client, mixedRequest())
	if result.GetSuccess() || !strings.Contains(result.GetError(), "app admin needs "+string(edge.NeedEdgeMiddleware)) {
		t.Fatalf("Deploy() = %q, want admin refused for %s: its router forwards to the container and runs none of the edge's code, while web's router does", result.GetError(), edge.NeedEdgeMiddleware)
	}
}

func TestAProjectDeployedThroughARouterItsEdgeNowForwardsToIsRefusedBeforeItTouchesTheOldRouting(t *testing.T) {
	client, p := mixedServed(t)
	seedStack(t, p, environment.TierProduction, "shop", stackrecords.EdgeState{
		Kind: fake.KindRelay,
		Edge: edge.StackState{Slug: "shop", Tier: environment.TierProduction},
		Routers: map[router.Kind]router.StackState{
			fake.RouterDirect: {Slug: "shop", Tier: environment.TierProduction},
		},
		Apps: map[string]router.Kind{"admin": fake.RouterDirect},
	})

	result, _, err := deployStream(t, client, mixedRequest())

	message := result.GetError()
	if err != nil {
		message = err.Error()
	}
	if result.GetSuccess() || message == "" {
		t.Fatal("Deploy() succeeded, want it refused: the router held this project's routing inside the edge's own stack, and the edge now forwards to it")
	}
	for _, want := range []string{"admin", string(fake.RouterDirect), "ocel destroy"} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal %q does not name %q", message, want)
		}
	}
}

func TestAProjectDeployedThroughAForwardedRouterIsNotRefused(t *testing.T) {
	client, _ := mixedServed(t)

	first, _ := deploy(t, client, mixedRequest())
	second, _ := deploy(t, client, withContainerAdmin(deployRequest(), "abcdefabcdefabcdefabcdefabcdefab"))

	if !first.GetSuccess() || !second.GetSuccess() {
		t.Errorf("deploys = %q then %q, want a project already forwarded left alone", first.GetError(), second.GetError())
	}
}
