package providerserver_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestADeployRecordsTheRouterEachAppFlipsThrough(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	state := readStack(t, p, environment.TierProduction, "shop")
	relay := fake.RouterRelay
	if state.Apps["web"] != relay {
		t.Errorf("the edge state pairs web with %q, want %q: a later rollback or removal reads which router an app flips through from here", state.Apps["web"], relay)
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

func (e originEdges) Open(kind edge.Kind) (edge.Edge, error) {
	if kind != edge.None {
		return e.Edges.Open(kind)
	}
	front, err := e.Edges.Open(fake.KindDirect)
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

func TestEachAppStackIsHandedTheRouterItsAppFlipsThroughAndNotItsEdge(t *testing.T) {
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

func TestAnAppWhoseComputeNoRouterIsPairedForDeploysThroughItsEdgeAsBefore(t *testing.T) {
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, pairedForContainersOnly{p})
	bootstrappedOverRPC(t, client)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() of a serverless app on a provider that pairs its edge for containers only = %q, want it to deploy and flip through the edge's stack as it did before pairings were declared", result.GetError())
	}
	routed := p.Routers().(*fake.Routers).DataPlane(fake.RouterRelay).Builds("shop", environment.TierProduction, router.DefaultPointer)
	if routed["web"] == "" {
		t.Errorf("the relay edge routes %v on %s after the deploy, want web's build", routed, router.DefaultPointer)
	}
	if kind, paired := readStack(t, p, environment.TierProduction, "shop").Apps["web"]; paired {
		t.Errorf("the edge state pairs web with %q, and this provider pairs no router for serverless apps", kind)
	}
}

func TestAHostnameOfAnAppWhoseComputeNoRouterIsPairedForIsServedThroughTheRouterItsEdgeOpened(t *testing.T) {
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, pairedForContainersOnly{p})
	bootstrappedOverRPC(t, client)

	req := deployRequest()
	req.Edge = writtenBy("shop.example")
	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if !slices.Equal(servedURLs(result), []string{"https://shop.example"}) {
		t.Errorf("the deploy served %v, want shop.example: web flips through %s, the router its edge opened, so that router answering the hostname serves it", servedURLs(result), fake.RouterRelay)
	}
	if host := readStack(t, p, environment.TierProduction, "shop").Host("shop.example"); !host.Probe.OK || host.Probe.Router != fake.RouterRelay {
		t.Errorf("shop.example is recorded %+v, want it answered by %s", host, fake.RouterRelay)
	}
}
