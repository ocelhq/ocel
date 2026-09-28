package fake_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/router/routerconformance"
)

func TestEveryFakeRouterBehavesAsEveryRouterMust(t *testing.T) {
	for _, kind := range []edge.Kind{fake.KindRelay, fake.KindDirect} {
		t.Run(string(kind), func(t *testing.T) {
			routerconformance.Run(t, routerconformance.Suite{
				New:      func(t *testing.T) routerconformance.Fixture { return fakeFixture(t, kind) },
				Hostname: "shop.example.com",
			})
		})
	}
}

func fakeFixture(t *testing.T, kind edge.Kind) routerconformance.Fixture {
	t.Helper()
	p := fake.NewProvider(fake.Options{})
	spec := edge.StackSpec{Tier: environment.TierProduction, Slug: "conformance"}
	front, err := p.Edges().Open(kind)
	if err != nil {
		t.Fatalf("Edges().Open(%q): %v", kind, err)
	}
	stack, err := front.Reconcile(context.Background(), spec, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile the edge: %v", err)
	}
	routes, err := p.Routers().Open(router.Kind(kind))
	if err != nil {
		t.Fatalf("Routers().Open(%q): %v", kind, err)
	}
	edges := p.Edges().(*fake.Edges)
	return routerconformance.Fixture{
		Router: routes,
		Spec:   router.StackSpec{Tier: spec.Tier, Slug: spec.Slug},
		Prior:  router.NewStackState(stack.State()),
		Serving: func(pointer string) string {
			return edges.Edge(kind).Routed(spec.Slug, spec.Tier, pointer)[routerconformance.App]
		},
		FailNextFlip: edges.Edge(kind).FailNextFlip,
	}
}
