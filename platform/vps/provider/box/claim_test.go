package box_test

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const pulled = "-----BEGIN CERTIFICATE-----\nthe zone's client certificate\n-----END CERTIFICATE-----\n"

func routedOn(t *testing.T) (*machine, router.Stack) {
	t.Helper()
	m, front, stack := reconciled(t)
	routed, err := box.NewRouter(front.Edge).Open(router.NewStackState(stack.State()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m, routed
}

func TestAClaimOnTheBoxShieldsTheHostnameBeforeItTakesItAndNamesTheBoxAsItsOrigin(t *testing.T) {
	m, routed := routedOn(t)

	origin, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", ClientCertificates: []string{pulled}})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if origin != (edge.Origin{Address: address}) {
		t.Errorf("Claim named origin %+v, want the box at %s: the edge in front forwards the hostname there", origin, address)
	}
	surface := box.Surface(slug, environment.TierProduction)
	want := host.Shield{Hostname: "shop.example.com", Owner: surface, ClientCertificates: []string{pulled}}
	if len(m.shields) != 1 || !reflect.DeepEqual(m.shields[0], want) {
		t.Errorf("the box shields %+v, want %+v: the proxy answers the hostname only to a client presenting that certificate", m.shields, want)
	}
	if !slices.Contains(m.claims, host.HostClaim{Owner: surface, Hostname: "shop.example.com", Pointer: router.DefaultPointer, App: "web"}) {
		t.Errorf("the box claims %+v, want shop.example.com among them", m.claims)
	}
	if shielded, claimed := slices.Index(m.calls, "shield shop.example.com"), slices.Index(m.calls, "claim shop.example.com"); shielded < 0 || shielded > claimed {
		t.Errorf("calls = %v, want the hostname shielded before it is claimed: a claimed hostname nothing shields is answered to anyone", m.calls)
	}
	if !slices.Contains(m.calls, "apply origins "+slug+"/"+string(environment.TierProduction)) {
		t.Errorf("calls = %v, want the project's buckets brought in line with the hostname it now claims", m.calls)
	}
}

func TestDisclaimingOnTheBoxGivesTheHostnameAndItsShieldBack(t *testing.T) {
	m, routed := routedOn(t)
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", ClientCertificates: []string{pulled}}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if err := routed.Disclaim(context.Background(), "shop.example.com"); err != nil {
		t.Fatalf("Disclaim: %v", err)
	}
	if slices.ContainsFunc(m.claims, func(claim host.HostClaim) bool { return claim.Hostname == "shop.example.com" }) {
		t.Errorf("the box still claims %+v, want shop.example.com given back", m.claims)
	}
	if len(m.shields) != 0 {
		t.Errorf("the box still shields %+v, want nothing left of shop.example.com", m.shields)
	}
}

func TestDestroyingTheBoxRouterLeavesNothingClaimedRoutedOrNetworked(t *testing.T) {
	m, routed := routedOn(t)
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web"}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if err := routed.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(m.claims) != 0 {
		t.Errorf("the box still claims %+v after the router was destroyed", m.claims)
	}
	surface := box.Surface(slug, environment.TierProduction)
	for _, call := range []string{"unroute " + surface, "forget network " + string(environment.TierProduction) + "/" + slug} {
		if !slices.Contains(m.calls, call) {
			t.Errorf("calls = %v, want %q: an edge in front forwards to the box and takes none of it down", m.calls, call)
		}
	}
}

func TestTheBoxRoutersRemovalPlanNamesTheRouteOfEveryHostnameAnEdgeForwardsToIt(t *testing.T) {
	_, front, _ := reconciled(t)

	var routes []string
	for _, group := range box.NewRouter(front.Edge).ProjectRemovals(edge.ProjectScope{
		Slug: slug, Tier: environment.TierProduction, Hostnames: []string{"shop.example.com"}, Front: address,
	}) {
		for _, change := range group.Changes {
			if change.Kind == box.RouteKind && change.Action == edge.PlanDelete {
				routes = append(routes, change.Name)
			}
		}
	}
	if !slices.Equal(routes, []string{"shop.example.com"}) {
		t.Errorf("the box router plans to delete routes %v, want shop.example.com: an edge in front lists only what it forwards, and the plan must name what the box takes down", routes)
	}
}
