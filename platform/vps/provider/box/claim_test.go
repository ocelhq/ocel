package box_test

import (
	"context"
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

func TestAClaimOnTheBoxTakesTheHostnameShieldedByTheClientCertificateAndNamesTheBoxAsItsOrigin(t *testing.T) {
	m, routed := routedOn(t)

	origin, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", ClientCertificate: pulled})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if origin != (edge.Origin{Address: address}) {
		t.Errorf("Claim named origin %+v, want the box at %s: the edge in front forwards the hostname there", origin, address)
	}
	want := host.HostClaim{
		Owner: box.Surface(slug, environment.TierProduction), Hostname: "shop.example.com",
		Pointer: router.DefaultPointer, App: "web", ClientCertificate: pulled,
	}
	if !slices.Contains(m.claims, want) {
		t.Errorf("the box claims %+v, want %+v among them: the proxy answers the hostname only to a client presenting that certificate", m.claims, want)
	}
	if !slices.Contains(m.calls, "apply origins "+slug+"/"+string(environment.TierProduction)) {
		t.Errorf("calls = %v, want the project's buckets brought in line with the hostname it now claims", m.calls)
	}
}

func TestDisclaimingOnTheBoxGivesTheHostnameBack(t *testing.T) {
	m, routed := routedOn(t)
	if _, err := routed.Claim(context.Background(), router.Claim{Hostname: "shop.example.com", App: "web", ClientCertificate: pulled}); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if err := routed.Disclaim(context.Background(), "shop.example.com"); err != nil {
		t.Fatalf("Disclaim: %v", err)
	}
	if slices.ContainsFunc(m.claims, func(claim host.HostClaim) bool { return claim.Hostname == "shop.example.com" }) {
		t.Errorf("the box still claims %+v, want shop.example.com given back", m.claims)
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
