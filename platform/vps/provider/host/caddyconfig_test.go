package host

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

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
		Grace:    30 * time.Second,
		Routes:   []AppRoute{{App: "web", Upstream: "shop-web-2222:" + AppPort}},
		Retiring: "shop-web-1111:" + AppPort,
	}
}

func TestEveryConfigTheRendererEmitsCarriesTheAdminBlockItsGuardWouldRefuseItFor(t *testing.T) {
	t.Parallel()

	for what, state := range map[string]ProxyState{
		"a flip":               releasing(),
		"the steady state":     {Grace: 30 * time.Second, Routes: releasing().Routes},
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

func TestTheDrainServerBindsTheProxysOwnLoopbackAndCarriesNothingButTheRetiredUpstream(t *testing.T) {
	t.Parallel()

	found := servers(t, loading(t, releasing()))
	drain, _ := found[proxyDrainServer].(map[string]any)
	if drain == nil {
		t.Fatalf("a flip renders no %s server, and the retired upstream then leaves the pool the moment the flip lands", proxyDrainServer)
	}
	listen, _ := drain["listen"].([]any)
	if len(listen) != 1 || listen[0] != drainListen {
		t.Errorf("%s listens on %v, want %s alone: it is reachable from nothing outside the proxy's own network namespace",
			proxyDrainServer, listen, drainListen)
	}
	if dialled := strings.Count(string(mustRender(t, releasing())), releasing().Retiring); dialled != 1 {
		t.Errorf("the retired upstream is dialled %d times, want once, on the drain server alone", dialled)
	}
}

func TestTheSteadyStateIsTheFlipWithoutTheDrainServer(t *testing.T) {
	t.Parallel()

	steady := releasing()
	steady.Retiring = ""
	found := servers(t, loading(t, steady))
	if _, stood := found[proxyDrainServer]; stood {
		t.Errorf("the steady state still declares %s, and the retired container stays declared after it is stopped", proxyDrainServer)
	}
	if live, _ := found[proxyServer].(map[string]any); live == nil {
		t.Fatalf("the steady state declares no %s server at all: %v", proxyServer, found)
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
		{App: "worker", Upstream: "shop-worker-1:" + AppPort},
		{App: "web", Upstream: "shop-web-1:" + AppPort},
	}}
	ordered := ProxyState{Grace: 30 * time.Second, Routes: []AppRoute{
		{App: "web", Upstream: "shop-web-1:" + AppPort},
		{App: "worker", Upstream: "shop-worker-1:" + AppPort},
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
	if read.Retiring != "" {
		t.Errorf("the retiring upstream read back as %q, and a drain server is a property of one flip rather than of the box", read.Retiring)
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

func mustRender(t *testing.T, state ProxyState) []byte {
	t.Helper()
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	return rendered
}
