package host

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
)

const previewSurface = "ocel--shop--preview"

func previewKey(pointer, app string) boxapi.RouteKey {
	return boxapi.RouteKey{Owner: previewSurface, Pointer: pointer, App: app}
}

func previewClaim(pointer, app, hostname string) boxapi.Claim {
	return boxapi.Claim{Hostname: hostname, Owner: previewSurface, Pointer: pointer, App: app}
}

func twoBranchesOfOneApp() boxapi.RoutingTable {
	return boxapi.RoutingTable{
		Grace:       routing.DrainWindow,
		PreviewBase: previewBase,
		Routes: []boxapi.Route{
			{RouteKey: previewKey("pr-7", "web"), Upstream: "shop-web-7:" + containerimage.PortText},
			{RouteKey: previewKey("pr-9", "web"), Upstream: "shop-web-9:" + containerimage.PortText},
		},
		Claims: []boxapi.Claim{
			previewClaim("pr-7", "", "shop--pr-7."+previewBase),
			previewClaim("pr-9", "", "shop--pr-9."+previewBase),
		},
	}
}

func TestTwoLivePreviewsOfOneAppAnswerOnTheirOwnHostnameEach(t *testing.T) {
	t.Parallel()

	rendered, err := routing.RenderProxyConfig(caddy.Builtin{}, twoBranchesOfOneApp())
	if err != nil {
		t.Fatalf("RenderProxyConfig(caddy.Builtin{}, ) = %v: two branches of one app are the ordinary preview case, and a claim keyed on the surface and the app alone hands both hostnames to both routes", err)
	}
	read, err := routing.ReadRoutingTable(mustWrite(t, twoBranchesOfOneApp()))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	want := slices.SortedFunc(slices.Values(twoBranchesOfOneApp().Claims), func(a, b boxapi.Claim) int {
		return strings.Compare(a.Hostname, b.Hostname)
	})
	if !slices.Equal(read.Claims, want) {
		t.Fatalf("the claims read back as %v, want %v: the pointer is what tells one branch's hostname from another's", read.Claims, want)
	}
	if len(rendered) == 0 {
		t.Fatal("the front proxy's config rendered empty")
	}
	answer := routedBy(t, twoBranchesOfOneApp())
	for _, claim := range twoBranchesOfOneApp().Claims {
		at := slices.IndexFunc(twoBranchesOfOneApp().Routes, func(route boxapi.Route) bool { return route.Pointer == claim.Pointer })
		if upstream, ok := answer(claim.Hostname, "/"); !ok || upstream != twoBranchesOfOneApp().Routes[at].Upstream {
			t.Errorf("%s is answered from %q (%v), want branch %s's own app: a hostname answered by the other branch takes a live preview off the air",
				claim.Hostname, upstream, ok, claim.Pointer)
		}
	}
}

func TestAClaimNamingNoPointerIsRefusedRatherThanAnsweredForEveryBranch(t *testing.T) {
	t.Parallel()

	state := twoBranchesOfOneApp()
	state.Claims[0].Pointer = ""
	if _, err := routing.RenderProxyConfig(caddy.Builtin{}, state); err == nil {
		t.Error("a claim naming no pointer rendered, and a box runs many branches of one app at once: the pointer is half of what says which route answers a hostname")
	}
	if err := routing.RefuseInvalid(boxapi.RoutingTable{Claims: []boxapi.Claim{previewClaim("pr"+boxapi.ClaimSeparator+"7", "", "shop--pr-7."+previewBase)}}); err == nil {
		t.Errorf("a pointer containing %q is claimable, and it is what separates the fields of the identity this is written under", boxapi.ClaimSeparator)
	}
}

func TestAProductionBindClaimsUnderTheDefaultPointerAndKeepsItsRoute(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: router.DefaultPointer}}
	read, err := routing.ReadRoutingTable(mustWrite(t, state))
	if err != nil {
		t.Fatalf("ReadRoutingTable() = %v", err)
	}
	if !slices.Equal(read.Claims, state.Claims) {
		t.Fatalf("the claims read back as %v, want %v", read.Claims, state.Claims)
	}
	if _, ok := routedBy(t, state)(claimed, "/"); !ok {
		t.Fatal("the box answers nothing for a hostname claimed under the default pointer")
	}
}
