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
