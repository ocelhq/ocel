package providerserver_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestADeployFlipsItsPromotionThroughTheRouterItsEdgePairsWith(t *testing.T) {
	builtProject(t)
	client, p := deployServed(t)

	result, _ := deploy(t, client, deployRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want it to succeed", result.GetError())
	}
	routed := p.Edges().(*fake.Edges).Edge(fake.KindRelay).Routed("shop", environment.TierProduction, router.DefaultPointer)
	if routed["web"] == "" {
		t.Errorf("the relay router routes %v on %s after the deploy, want web's build: the promotion is flipped through the router its edge pairs with", routed, router.DefaultPointer)
	}
}

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
