package host

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func storing() RoutingTable {
	return RoutingTable{
		Grace: DrainWindow,
		Routes: []AppRoute{
			{RouteKey: keyed("web"), Upstream: "shop-web-1:" + appbuild.InjectedPortText},
			{RouteKey: keyed(switchboard.StoreLabel), Upstream: "shop-prod-store-s3:9000"},
		},
		Claims: []HostClaim{
			{Hostname: "shop.example.com", Owner: surface, Pointer: pointed},
			{Hostname: "storage.shop.example.com", Owner: surface, Pointer: pointed, App: switchboard.StoreLabel},
		},
	}
}

func TestAStoreRouteAnswersItsOwnHostnameBesideAnAppClaimingTheProjectsOwn(t *testing.T) {
	t.Parallel()

	state := storing()
	answer := routedBy(t, state)
	if upstream, ok := answer("shop.example.com", "/"); !ok || upstream != state.Routes[0].Upstream {
		t.Errorf("the project's own hostname is answered from %q (%v), want the app it runs", upstream, ok)
	}
	if upstream, ok := answer("storage.shop.example.com", "/bucket/key"); !ok || upstream != state.Routes[1].Upstream {
		t.Errorf("the store's hostname is answered from %q (%v), want the store alone: it is not an app and never answers the project's own name", upstream, ok)
	}
}

func TestAStoreRouteIsNotAnAppTheProjectsOwnHostnameCouldBeAmbiguousBetween(t *testing.T) {
	t.Parallel()

	state := storing()
	state.Routes = append(state.Routes, AppRoute{RouteKey: keyed("api"), Upstream: "shop-api-1:" + appbuild.InjectedPortText})
	if _, err := RenderProxyConfig(caddy.Builtin{}, state); err == nil {
		t.Fatal("two apps under one wide claim rendered, and whichever sorted first would answer for both")
	}

	state.Routes = state.Routes[:len(state.Routes)-1]
	if _, err := RenderProxyConfig(caddy.Builtin{}, state); err != nil {
		t.Fatalf("one app and a store under a wide claim = %v, want the app to answer it", err)
	}
}

func TestAStorePresentBeforeAnyDomainIsBoundIsATableTheBoxServes(t *testing.T) {
	t.Parallel()

	state := storing()
	state.Claims = nil
	ask := probing(t, state)
	if answer := ask("nothing.example.com"); answer.status != 404 {
		t.Errorf("a box routing a store nothing claims a name for answered %d", answer.status)
	}
}
