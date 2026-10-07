package fake_test

import (
	"context"
	"slices"
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
				Previews:    func(t *testing.T) routerconformance.Fixture { return fakeFixture(t, kind, routedBy) },
				Hostname:    "shop.example.com",
				PreviewBase: "preview.example.com",
				Tunnel:      kind,
			})
		})
	}
}

func TestAFakePointerMoveStopsServingTheHostnamesItSupersedesAndNoOthers(t *testing.T) {
	fixture, plane := openFakeRouter(t, fake.KindRelay, fake.RouterRelay)
	p := fixture.Router
	stack, err := p.Reconcile(context.Background(), fixture.Spec, fixture.Prior)
	if err != nil {
		t.Fatal(err)
	}
	hostsOf := func(hostnames ...string) []edge.PreviewHost {
		hosts := make([]edge.PreviewHost, 0, len(hostnames))
		for _, hostname := range hostnames {
			hosts = append(hosts, edge.PreviewHost{Hostname: hostname, App: "web"})
		}
		return hosts
	}
	move := func(hosts, superseded []edge.PreviewHost) {
		t.Helper()
		if err := stack.MovePointer(context.Background(), router.PointerMove{
			Pointer:    "pr-7",
			Promotion:  router.Promotion{PromotionID: "p1", Releases: map[string]string{"web": "b1"}},
			Records:    map[string]router.ReleaseRecord{"web": {App: "web", Release: "b1"}},
			Hosts:      hosts,
			Superseded: superseded,
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	move(hostsOf("old.preview.example.com", "kept.preview.example.com"), nil)
	move(hostsOf("new.preview.example.com"), hostsOf("old.preview.example.com"))
	want := []string{"kept.preview.example.com", "new.preview.example.com"}
	if got := plane.ListServedHostnames(); !slices.Equal(got, want) {
		t.Errorf("served hostnames = %v, want %v: a move withdraws what it supersedes and leaves the rest to RemovePointer", got, want)
	}
}

func fakeFixture(t *testing.T, kind edge.Kind, routedBy router.Kind) routerconformance.Fixture {
	t.Helper()
	fixture, _ := openFakeRouter(t, kind, routedBy)
	return fixture
}

func openFakeRouter(t *testing.T, kind edge.Kind, routedBy router.Kind) (routerconformance.Fixture, *fake.DataPlane) {
	t.Helper()
	p := fake.NewProvider(fake.Options{})
	spec := edge.StackSpec{Tier: environment.TierProduction, Slug: "conformance"}
	front, err := p.Edges().Open(kind, nil)
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
			return plane.Releases(spec.Slug, spec.Tier, pointer)[routerconformance.App]
		},
		FailNextPointerMove: plane.FailNextPointerMove,
	}, plane
}
