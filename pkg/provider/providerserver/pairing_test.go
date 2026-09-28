package providerserver_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
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
	relay := router.Kind(fake.KindRelay)
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
	routed := p.Edges().(*fake.Edges).Edge(fake.KindRelay).Routed("shop", environment.TierProduction, router.DefaultPointer)
	if routed["web"] == "" {
		t.Errorf("the relay edge routes %v on %s after the deploy, want web's build", routed, router.DefaultPointer)
	}
	if kind, paired := readStack(t, p, environment.TierProduction, "shop").Apps["web"]; paired {
		t.Errorf("the edge state pairs web with %q, and this provider pairs no router for serverless apps", kind)
	}
}

type pairedWithAnotherKind struct{ *fake.Provider }

func (p pairedWithAnotherKind) Facts() provider.Facts {
	facts := p.Provider.Facts()
	for i := range facts.Pairings {
		facts.Pairings[i].Router = "another"
	}
	return facts
}

func TestAnAppPairedWithARouterOfAnotherKindDeploysThroughItsEdgeAsBefore(t *testing.T) {
	builtProject(t)
	p := fake.NewProvider(fake.Options{})
	client := servedBy(t, pairedWithAnotherKind{p})
	bootstrappedOverRPC(t, client)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() on a provider that pairs its edge with a router of another kind = %q, want it to deploy and flip through the edge's stack as it did before pairings were declared", result.GetError())
	}
	routed := p.Edges().(*fake.Edges).Edge(fake.KindRelay).Routed("shop", environment.TierProduction, router.DefaultPointer)
	if routed["web"] == "" {
		t.Errorf("the relay edge routes %v on %s after the deploy, want web's build", routed, router.DefaultPointer)
	}
}
