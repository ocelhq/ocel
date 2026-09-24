package host

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
)

func loading(t *testing.T, state ProxyState) map[string]any {
	t.Helper()
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	if err := caddyadmin.Keeps(rendered, ProxyAdminSocket); err != nil {
		t.Fatalf("the renderer emitted a config the flip refuses: %v\n%s", err, rendered)
	}
	var read map[string]any
	if err := json.Unmarshal(rendered, &read); err != nil {
		t.Fatal(err)
	}
	return read
}

func servers(t *testing.T, read map[string]any) map[string]any {
	t.Helper()
	apps, _ := read["apps"].(map[string]any)
	http, _ := apps["http"].(map[string]any)
	found, _ := http["servers"].(map[string]any)
	if found == nil {
		t.Fatalf("the rendered config carries no http servers: %v", read)
	}
	return found
}

func releasing() ProxyState {
	return ProxyState{
		Grace:  30 * time.Second,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: "shop-web-2222:" + providerkit.InjectedPortText}},
	}
}

func TestEveryConfigTheRendererEmitsCarriesTheAdminBlockItsGuardWouldRefuseItFor(t *testing.T) {
	t.Parallel()

	for what, state := range map[string]ProxyState{
		"a flip":               releasing(),
		"a box serving no app": {Grace: 30 * time.Second},
	} {
		read := loading(t, state)
		admin, _ := read["admin"].(map[string]any)
		if admin == nil {
			t.Fatalf("%s renders no admin block, and caddy applies that section before it validates the rest", what)
		}
		if listen := admin["listen"]; listen != caddyadmin.Listen(ProxyAdminSocket) {
			t.Errorf("%s declares the admin endpoint at %v, want %s", what, listen, caddyadmin.Listen(ProxyAdminSocket))
		}
	}
}

func TestTheProxysCeilingAndTheDeploysAreOneNumber(t *testing.T) {
	t.Parallel()

	state := releasing()
	state.Grace = 12 * time.Second
	read := loading(t, state)
	apps, _ := read["apps"].(map[string]any)
	http, _ := apps["http"].(map[string]any)
	if grace := http["grace_period"]; grace != "12s" {
		t.Errorf("the rendered config declares a grace period of %v, want the drain window it was given: caddy's default is eternal", grace)
	}
}

func TestARedeployThatChangesNothingRendersTheSameBytes(t *testing.T) {
	t.Parallel()

	scrambled := ProxyState{Grace: 30 * time.Second, Routes: []AppRoute{
		{RouteKey: keyed("worker"), Upstream: "shop-worker-1:" + providerkit.InjectedPortText},
		{RouteKey: keyed("web"), Upstream: "shop-web-1:" + providerkit.InjectedPortText},
	}}
	ordered := ProxyState{Grace: 30 * time.Second, Routes: []AppRoute{
		{RouteKey: keyed("web"), Upstream: "shop-web-1:" + providerkit.InjectedPortText},
		{RouteKey: keyed("worker"), Upstream: "shop-worker-1:" + providerkit.InjectedPortText},
	}}
	if !bytes.Equal(mustRender(t, scrambled), mustRender(t, ordered)) {
		t.Error("two renders of the same set of apps differ by the order they were handed in, and a no-op deploy then rewrites the box's config")
	}
}

func TestTheBoxsOwnFileReadsBackAsTheStateThatRenderedIt(t *testing.T) {
	t.Parallel()

	state := releasing()
	read, err := ReadProxyState(mustRender(t, state))
	if err != nil {
		t.Fatalf("ReadProxyState() = %v", err)
	}
	if read.Grace != state.Grace {
		t.Errorf("the grace period read back as %s, want %s", read.Grace, state.Grace)
	}
	if len(read.Routes) != 1 || read.Routes[0] != state.Routes[0] {
		t.Errorf("the routes read back as %v, want %v: a deploy that cannot read the box's other apps overwrites them", read.Routes, state.Routes)
	}
}

func TestTheBaselineBootstrapSeedsReadsBackAsABoxServingNothing(t *testing.T) {
	t.Parallel()

	read, err := ReadProxyState(proxyBaseline)
	if err != nil {
		t.Fatalf("ReadProxyState() over the config bootstrap seeds = %v", err)
	}
	if len(read.Routes) != 0 {
		t.Errorf("the seeded baseline reads back carrying %v", read.Routes)
	}
	if read.Grace == 0 {
		t.Error("the seeded baseline reads back with no grace period, and rendering over it would declare caddy's eternal default")
	}
}

func TestTheBoxIssuesItsOwnRootBeforeAnyHostnameAsksForIt(t *testing.T) {
	t.Parallel()

	var seed, flipped map[string]any
	if err := json.Unmarshal(proxyBaseline, &seed); err != nil {
		t.Fatal(err)
	}
	apps, _ := seed["apps"].(map[string]any)
	pki, _ := json.Marshal(apps["pki"])
	if !bytes.Contains(pki, []byte(`"local"`)) {
		t.Fatalf("the seeded baseline declares no local certificate authority: %s", pki)
	}
	if err := json.Unmarshal(mustRender(t, releasing()), &flipped); err != nil {
		t.Fatal(err)
	}
	rendered, _ := json.Marshal(flipped["apps"].(map[string]any)["pki"])
	if !bytes.Equal(pki, rendered) {
		t.Errorf("the flip renders the pki app as\n%s\nwant the one the box was bootstrapped with\n%s", rendered, pki)
	}
}

func TestTheRedactingLogIsCarriedRatherThanRebuiltOnEveryFlip(t *testing.T) {
	t.Parallel()

	rendered := mustRender(t, releasing())
	var seed, flipped map[string]any
	if err := json.Unmarshal(proxyBaseline, &seed); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rendered, &flipped); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(seed["logging"])
	got, _ := json.Marshal(flipped["logging"])
	if !bytes.Equal(want, got) {
		t.Errorf("the flip renders the logging block as\n%s\nwant the redacting one the box was bootstrapped with\n%s", got, want)
	}
}

func keyed(app string) RouteKey { return RouteKey{Owner: surface, Pointer: pointed, App: app} }

func mustRender(t *testing.T, state ProxyState) []byte {
	t.Helper()
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	return rendered
}

func TestAReloadLeavesEveryProxiedStreamTheDrainWindowRatherThanCuttingIt(t *testing.T) {
	t.Parallel()

	state := storing()
	state.Grace = 12 * time.Second
	state.Connector = "box.example.com"
	forwards := 0
	for _, route := range servers(t, loading(t, state))[proxyServer].(map[string]any)["routes"].([]any) {
		for _, handler := range route.(map[string]any)["handle"].([]any) {
			handled := handler.(map[string]any)
			if handled["handler"] != caddyadmin.ForwardHandler {
				continue
			}
			forwards++
			if delay := handled["stream_close_delay"]; delay != "12s" {
				t.Errorf("route %v forwards with stream_close_delay %v, want the 12s drain window: caddy closes every websocket a reverse_proxy holds the moment a config load unloads it, so binding one domain cuts every stream on the box",
					route.(map[string]any)["@id"], delay)
			}
		}
	}
	if forwards < 3 {
		t.Fatalf("found %d forwarding handlers, want the connector, the store and an app", forwards)
	}
}
