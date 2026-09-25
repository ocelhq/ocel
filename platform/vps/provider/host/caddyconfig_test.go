package host

import (
	"bytes"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

func releasing() RoutingTable {
	return RoutingTable{
		Grace:  30 * time.Second,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: "shop-web-2222:" + providerkit.InjectedPortText}},
	}
}

func TestTheFrontProxysConfigHoldsStillThroughEveryReleaseAndDrainWindow(t *testing.T) {
	t.Parallel()

	before := releasing()
	before.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	after := before
	after.Grace = 3 * time.Second
	after.Routes = []AppRoute{{RouteKey: keyed("web"), Upstream: "shop-web-3333:" + providerkit.InjectedPortText}}
	if !bytes.Equal(mustRender(t, before), mustRender(t, after)) {
		t.Error("a release that moves a route and drains for a different window renders the front proxy's config anew, and every reload drops requests on every hostname the box serves")
	}
}

func TestARedeployThatChangesNothingRendersTheSameBytes(t *testing.T) {
	t.Parallel()

	scrambled := RoutingTable{Grace: 30 * time.Second, Routes: []AppRoute{
		{RouteKey: keyed("worker"), Upstream: "shop-worker-1:" + providerkit.InjectedPortText},
		{RouteKey: keyed("web"), Upstream: "shop-web-1:" + providerkit.InjectedPortText},
	}, Claims: []HostClaim{
		{Hostname: "www.example.com", Owner: surface, Pointer: pointed, App: "web"},
		{Hostname: claimed, Owner: surface, Pointer: pointed, App: "worker"},
	}}
	ordered := scrambled
	ordered.Routes = []AppRoute{scrambled.Routes[1], scrambled.Routes[0]}
	ordered.Claims = []HostClaim{scrambled.Claims[1], scrambled.Claims[0]}
	if !bytes.Equal(mustRender(t, scrambled), mustRender(t, ordered)) {
		t.Error("two renders of the same set of apps differ by the order they were handed in, and a no-op deploy then rewrites the box's config")
	}
}

func TestWhatBootstrapSeedsIsABoxServingNothingAndItsRendering(t *testing.T) {
	t.Parallel()

	read, err := ReadRoutingTable(routingTableItem().Content)
	if err != nil {
		t.Fatalf("ReadRoutingTable() over the table bootstrap seeds = %v", err)
	}
	if len(read.Routes) != 0 || len(read.Claims) != 0 || len(read.Pins) != 0 || read.PreviewBase != "" || read.Connector != "" {
		t.Errorf("the seeded table reads back carrying %+v", read)
	}
	if read.Grace != DrainWindow {
		t.Errorf("the seeded table reads back with a grace period of %s, want the %s drain window", read.Grace, DrainWindow)
	}
	if seeded := proxyConfigItem().Content; !bytes.Equal(seeded, mustRender(t, read)) {
		t.Errorf("bootstrap seeds %s as\n%s\nwhich is not the rendering of the table it seeds beside it, so a fresh box serves what no table records until its first write puts it back", ProxyConfig, seeded)
	}
}

func keyed(app string) RouteKey { return RouteKey{Owner: surface, Pointer: pointed, App: app} }

func mustRender(t *testing.T, state RoutingTable) []byte {
	t.Helper()
	rendered, err := RenderProxyConfig(caddy.Builtin{}, state)
	if err != nil {
		t.Fatalf("RenderProxyConfig(caddy.Builtin{}, ) = %v", err)
	}
	return rendered
}
