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
	for kind, routedBy := range map[edge.Kind]router.Kind{fake.KindRelay: fake.RouterRelay, fake.KindDirect: fake.RouterDirect} {
		t.Run(string(kind), func(t *testing.T) {
			routerconformance.Run(t, routerconformance.Suite{
				New:         func(t *testing.T) routerconformance.Fixture { return fakeFixture(t, kind, routedBy) },
				Hostname:    "shop.example.com",
				PreviewBase: "preview.example.com",
				Tunnel:      kind,
			})
		})
	}
}

func fakeFixture(t *testing.T, kind edge.Kind, routedBy router.Kind) routerconformance.Fixture {
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
	opened, err := p.Routers().Open(routedBy)
	if err != nil {
		t.Fatalf("Routers().Open(%q): %v", routedBy, err)
	}
	plane := p.Routers().(*fake.Routers).DataPlane(routedBy)
	return routerconformance.Fixture{
		Router: opened,
		Spec:   router.StackSpec{Tier: spec.Tier, Slug: spec.Slug},
		Prior:  router.NewStackState(stack.State()),
		Serving: func(pointer string) string {
			return plane.Builds(spec.Slug, spec.Tier, pointer)[routerconformance.App]
		},
		FailNextPointerMove: plane.FailNextPointerMove,
	}
}
