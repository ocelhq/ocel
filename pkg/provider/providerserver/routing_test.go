package providerserver_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
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

type lookingUpProvider struct{ *fake.Provider }

func (p lookingUpProvider) Routers() provider.Routers {
	return lookingUpRouters{Routers: p.Provider.Routers()}
}

type lookingUpRouters struct{ provider.Routers }

func (r lookingUpRouters) Open(kind router.Kind) (router.Router, error) {
	opened, err := r.Routers.Open(kind)
	return lookingUpRouter{Router: opened}, err
}

type lookingUpRouter struct{ router.Router }

func (r lookingUpRouter) Open(state router.StackState) (router.Stack, error) {
	opened, err := r.Router.Open(state)
	return &lookingUpStack{Stack: opened}, err
}

type lookingUpStack struct {
	router.Stack
	lookedUp bool
}

func (s *lookingUpStack) Ledger() router.Ledger {
	s.lookedUp = true
	return s.Stack.Ledger()
}

func (s *lookingUpStack) State() router.StackState {
	state := s.Stack.State()
	if s.lookedUp {
		state.Edge.Private = edge.Own(map[string]string{"stateTable": "looked-up"})
	}
	return state
}

func TestAPruneSavesWhatTheRouterLookedUpToReachItsLedger(t *testing.T) {
	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedProvider(t, "1.0.0", lookingUpProvider{vendor})
	deployed(t, vendor, environment.TierProduction, "shop")

	stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug:        "shop",
		KeepN:       1,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
	})
	if err != nil {
		t.Fatalf("RemoveStalePromotions() error = %v", err)
	}
	if result, err := drain(stream); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", result.GetError(), err)
	}

	var saved map[string]string
	if err := readStack(t, vendor, environment.TierProduction, "shop").Edge.Private.Into(&saved); err != nil {
		t.Fatal(err)
	}
	if saved["stateTable"] != "looked-up" {
		t.Errorf("the saved edge state keeps %v, want the state table the router looked up to reach its ledger, so the next run need not look it up again", saved)
	}
}
