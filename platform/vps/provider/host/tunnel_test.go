package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const tunnelEdge edge.Kind = "cloudflare"

func isBusy(err error) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeBusy
}

func readBack(t *testing.T, box *claimBench) RoutingTable {
	t.Helper()
	table, err := ReadRoutingTable([]byte(box.recorded))
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func tunneledBox(t *testing.T) (*claimBench, Tunnel) {
	t.Helper()
	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	state.Tunnel = &Tunnel{Edge: tunnelEdge, Name: "ocel-203-0-113-10-0a1b2c3d", ID: "5a6b7c8d-1", Address: "5a6b7c8d-1.cfargotunnel.com"}
	state.Tunneled = []TunneledHost{{Hostname: claimed, Owner: surface}}
	return claimingBox(t, state), *state.Tunnel
}

func TestABoxReservesOneTunnelNameForItsEdgeAndHandsTheSameOneBack(t *testing.T) {
	t.Parallel()
	box := claimingBox(t, routed())

	first, err := box.host().ReserveTunnel(context.Background(), tunnelEdge)
	if err != nil {
		t.Fatalf("ReserveTunnel() = %v", err)
	}
	again, err := box.host().ReserveTunnel(context.Background(), tunnelEdge)
	if err != nil {
		t.Fatalf("ReserveTunnel() again = %v", err)
	}

	if first.Edge != tunnelEdge || !strings.HasPrefix(first.Name, "ocel-203-0-113-10-") || again != first {
		t.Errorf("ReserveTunnel() = %+v then %+v, want one name for the box, recorded before any tunnel is opened under it: two runs claiming at once open one tunnel", first, again)
	}
	if recorded := readBack(t, box).Tunnel; recorded == nil || *recorded != first {
		t.Errorf("the routing table records tunnel %+v, want %+v", recorded, first)
	}
}

func TestABoxReachedThroughOneEdgesTunnelRefusesAnothers(t *testing.T) {
	t.Parallel()
	box, _ := tunneledBox(t)

	_, err := box.host().ReserveTunnel(context.Background(), "another-edge")

	if !isBusy(err) {
		t.Errorf("ReserveTunnel(another-edge) = %v, want it refused busy: one tunnel reaches one box", err)
	}
}

func TestABoxRefusesATunnelToAnEdgeItRunsNoConnectorFor(t *testing.T) {
	t.Parallel()
	box := claimingBox(t, routed())

	_, err := box.host().ReserveTunnel(context.Background(), "another-edge")

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Errorf("ReserveTunnel(another-edge) = %v, want it refused invalid: the box runs no connector for that edge", err)
	}
	if recorded := readBack(t, box).Tunnel; recorded != nil {
		t.Errorf("the routing table records tunnel %+v, want none reserved", recorded)
	}
}

func TestATunnelIsReservedWithTheVisitorHeadersItsEdgeSends(t *testing.T) {
	t.Parallel()
	box := claimingBox(t, routed())

	reserved, err := box.host().ReserveTunnel(context.Background(), tunnelEdge)
	if err != nil {
		t.Fatal(err)
	}

	if reserved.VisitorAddressHeader != "Cf-Connecting-Ip" || reserved.VisitorSchemeHeader != "Cf-Visitor" {
		t.Errorf("ReserveTunnel() = %+v, want the headers Cloudflare names the visitor in, which the switchboard trusts only on the tunnel listener", reserved)
	}
}

func TestATunnelRunsAsOcelTunnelFromAPinnedImageReadingARootOnlyTokenFile(t *testing.T) {
	t.Parallel()
	box := claimingBox(t, routed())
	reserved, err := box.host().ReserveTunnel(context.Background(), tunnelEdge)
	if err != nil {
		t.Fatal(err)
	}
	opened := reserved
	opened.ID, opened.Address = "5a6b7c8d-1", "5a6b7c8d-1.cfargotunnel.com"

	if err := box.host().RunTunnel(context.Background(), opened, tokenOf("the-tunnel-token")); err != nil {
		t.Fatalf("RunTunnel() = %v", err)
	}

	if recorded := readBack(t, box).Tunnel; recorded == nil || *recorded != opened {
		t.Errorf("the routing table records tunnel %+v, want %+v: its last leaver deletes it by that id", recorded, opened)
	}
	at := box.at(quoted("--name") + " " + quoted(TunnelContainer))
	if at < 0 {
		t.Fatalf("no container was run: %v", box.commands())
	}
	run := box.commands()[at]
	for _, want := range []string{quoted(TunnelContainer), quoted(tunnelConnectors[tunnelEdge].image), quoted("--no-autoupdate"), quoted("TUNNEL_TOKEN_FILE=" + tunnelTokenMounted), quoted("--network") + " " + quoted(TunnelNetwork)} {
		if !strings.Contains(run, want) {
			t.Errorf("the tunnel was run as %q, want %s in it", run, want)
		}
	}
	for kind, connector := range tunnelConnectors {
		if !strings.Contains(connector.image, "@sha256:") {
			t.Errorf("the %s tunnel runs %q, want it pinned by digest", kind, connector.image)
		}
	}
	if strings.Contains(run, "--publish") {
		t.Errorf("the tunnel was run publishing a port: %q; it only dials out", run)
	}
	for at, command := range box.commands() {
		if strings.Contains(command, "the-tunnel-token") {
			t.Errorf("command %d names the token: %q; it reaches the box on stdin only", at, command)
		}
	}
	written := slices.IndexFunc(box.fed, func(fed string) bool { return fed == "the-tunnel-token" })
	if written < 0 || !strings.Contains(box.commands()[written], words(renderTunnelTokenArgv("place-secret"))) {
		t.Errorf("the token was not placed root-only in %s from stdin: %v", TunnelDir, box.commands())
	}
	if written > at {
		t.Errorf("the tunnel was run before its token was written: %v", box.commands())
	}
}

func tokenOf(token string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return token, nil }
}

func TestATunnelAlreadyRunningIsLeftRunningAndItsTokenNeverRead(t *testing.T) {
	t.Parallel()
	box, reserved := tunneledBox(t)
	serves := box.answer
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "docker inspect") && strings.Contains(command, quoted(TunnelContainer)) {
			return session.Result{Stdout: "true " + reserved.ID + "\n"}, true
		}
		return serves(command)
	}

	read := false
	err := box.host().RunTunnel(context.Background(), reserved, func(context.Context) (string, error) {
		read = true
		return "the-tunnel-token", nil
	})
	if err != nil {
		t.Fatalf("RunTunnel() = %v", err)
	}

	if read || box.at(quoted("docker")+" "+quoted("run")) >= 0 || box.took("docker rm") >= 0 {
		t.Errorf("a tunnel already running under %s had its token read (%v) or its container replaced: %v; every restart drops what the tunnel carries for every project on the box", reserved.ID, read, box.commands())
	}
}

func TestATunnelIsRunOnlyUnderTheNameTheBoxReserved(t *testing.T) {
	t.Parallel()
	box, reserved := tunneledBox(t)
	stale := reserved
	stale.Name = "ocel-203-0-113-10-ffffffff"

	err := box.host().RunTunnel(context.Background(), stale, tokenOf("the-tunnel-token"))

	if !isBusy(err) || box.at(quoted("docker")+" "+quoted("run")) >= 0 {
		t.Errorf("RunTunnel() under a name the box no longer reserves = %v, running %v, want it refused busy with nothing run: that tunnel is being deleted", err, box.commands())
	}
}

func TestATunneledHostnameIsLeftOutOfTheProxyAndRefusedByTheSwitchboardAroundTheTunnel(t *testing.T) {
	t.Parallel()
	box := claimingBox(t, func() RoutingTable {
		state := routed()
		state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
		state.Tunnel = &Tunnel{Edge: tunnelEdge, Name: "ocel-203-0-113-10-0a1b2c3d", ID: "5a6b7c8d-1", Address: "5a6b7c8d-1.cfargotunnel.com"}
		return state
	}())

	if err := box.host().TunnelHost(context.Background(), TunneledHost{Hostname: claimed, Owner: surface}, "ocel-203-0-113-10-0a1b2c3d"); err != nil {
		t.Fatalf("TunnelHost() = %v", err)
	}

	table := readBack(t, box)
	if !slices.Equal(table.Tunneled, []TunneledHost{{Hostname: claimed, Owner: surface}}) {
		t.Errorf("the routing table tunnels %v, want %s for %s", table.Tunneled, claimed, surface)
	}
	if rendered := string(mustRender(t, table)); strings.Contains(rendered, claimed) {
		t.Errorf("the proxy config names %s, want it left out: the proxy on the box never answers a tunneled hostname, or asks for its certificate", claimed)
	}
	read, err := switchboard.Read(mustWrite(t, table))
	if err != nil {
		t.Fatal(err)
	}
	if !read.IsTunneled(claimed) || read.Admits(claimed) {
		t.Errorf("the switchboard reads %s as tunneled %v and admitted %v, want tunneled and never admitted", claimed, read.IsTunneled(claimed), read.Admits(claimed))
	}
	if !slices.ContainsFunc(box.commands(), loadsSwitchboard) {
		t.Errorf("the tunneled hostname was never loaded onto the switchboard: %v", box.commands())
	}
}

func TestAHostnameIsTunneledOnlyThroughTheTunnelTheBoxReserves(t *testing.T) {
	t.Parallel()
	box, _ := tunneledBox(t)

	err := box.host().TunnelHost(context.Background(), TunneledHost{Hostname: "blog.example.com", Owner: surface}, "ocel-203-0-113-10-ffffffff")

	if !isBusy(err) {
		t.Errorf("TunnelHost() through a tunnel the box no longer reserves = %v, want it refused busy", err)
	}
}

func TestTheTunnelIsReleasedOnlyOnceNoHostnameIsTunneled(t *testing.T) {
	t.Parallel()
	box, reserved := tunneledBox(t)

	if retired, err := box.host().ReleaseTunnel(context.Background()); err != nil || len(retired) != 0 {
		t.Fatalf("ReleaseTunnel() with %s tunneled = %v, %v; want nothing released", claimed, retired, err)
	}
	if err := box.host().RemoveTunneledHost(context.Background(), TunneledHost{Hostname: claimed, Owner: surface}); err != nil {
		t.Fatalf("RemoveTunneledHost() = %v", err)
	}
	retired, err := box.host().ReleaseTunnel(context.Background())
	if err != nil || !slices.Equal(retired, []Tunnel{reserved}) {
		t.Fatalf("ReleaseTunnel() = %+v, %v; want %+v released", retired, err, reserved)
	}

	table := readBack(t, box)
	if table.Tunnel != nil || !slices.Equal(table.Retired, []Tunnel{reserved}) {
		t.Errorf("the routing table records tunnel %+v and retired %+v, want none current and %+v retired until the edge deletes it", table.Tunnel, table.Retired, reserved)
	}
	if again, err := box.host().ReleaseTunnel(context.Background()); err != nil || !slices.Equal(again, []Tunnel{reserved}) {
		t.Errorf("ReleaseTunnel() again = %+v, %v; want the retired tunnel handed back until it is forgotten", again, err)
	}
	if err := box.host().ForgetTunnel(context.Background(), reserved.Name); err != nil {
		t.Fatalf("ForgetTunnel() = %v", err)
	}
	if table := readBack(t, box); len(table.Retired) != 0 {
		t.Errorf("the routing table still retires %+v once the edge deleted it", table.Retired)
	}
	if box.took("docker rm --force "+quoted(TunnelContainer)) < 0 || box.at(words(renderTunnelTokenArgv("unplace"))) < 0 {
		t.Errorf("the tunnel's container and token were not removed: %v", box.taking())
	}
}

func TestDisclaimingAHostnameOrASurfaceStopsTunnelingIt(t *testing.T) {
	t.Parallel()
	box, _ := tunneledBox(t)
	if err := box.host().DisclaimHost(context.Background(), claimed, surface); err != nil {
		t.Fatal(err)
	}
	if tunneled := readBack(t, box).Tunneled; len(tunneled) != 0 {
		t.Errorf("DisclaimHost() left %v tunneled", tunneled)
	}

	box, _ = tunneledBox(t)
	if err := box.host().DisclaimSurface(context.Background(), surface); err != nil {
		t.Fatal(err)
	}
	if tunneled := readBack(t, box).Tunneled; len(tunneled) != 0 {
		t.Errorf("DisclaimSurface() left %v tunneled", tunneled)
	}
}

func TestTheTunnelListenerIsBoundOnANetworkOnlyTheSwitchboardAndTheTunnelJoin(t *testing.T) {
	t.Parallel()
	joined := quoted("--network") + " " + quoted(TunnelNetwork)
	for what, command := range running() {
		if strings.Contains(command, joined) != (what == boardContainer || what == tunnelContainer) {
			t.Errorf("%s runs %q, want only the switchboard and the tunnel on %s: the tunnel listener trusts what the edge says about the visitor", what, command, TunnelNetwork)
		}
	}
	if tunnel := running()[tunnelContainer]; strings.Contains(tunnel, quoted("--network")+" "+quoted(ProxyNetwork)) {
		t.Errorf("the tunnel runs %q, want it off the %s network", tunnel, ProxyNetwork)
	}
	for name, front := range map[string]Front{
		"ocel's caddy":            {},
		"a manual proxy":          {Manual: &ManualFront{Port: 8080}},
		"a manual proxy on ocel":  {Manual: &ManualFront{Port: 8080, Network: ProxyNetwork}},
		"your caddy on a network": {Caddy: &CaddyFront{Directory: "/etc/caddy", Network: "coolify"}},
		"your traefik":            {Traefik: &TraefikFront{Directory: "/etc/traefik", Network: "coolify"}},
	} {
		board := switchboardBox(nil, front)
		command := words(board.command)
		if !strings.Contains(command, quoted("--tunnel-listen")+" "+quoted(TunnelNetwork+":"+switchboard.TunnelListenPort)) {
			t.Errorf("behind %s the switchboard runs %q, want its tunnel listener bound on the %s network only", name, command, TunnelNetwork)
		}
		for _, port := range board.ports {
			if port.target == switchboard.TunnelListenPort {
				t.Errorf("behind %s the switchboard publishes its tunnel listener as %v", name, port)
			}
		}
		if slices.ContainsFunc(front.joined(), func(joined userNetwork) bool { return joined.name == TunnelNetwork }) {
			t.Errorf("behind %s your proxy's network is %s", name, TunnelNetwork)
		}
		for what, script := range map[string]string{"started": board.writing(containerRising), "started again": board.restoring(containerRising)} {
			if !strings.Contains(script, "docker network create "+quoted(TunnelNetwork)) {
				t.Errorf("behind %s the switchboard is %s without %s created first:\n%s", name, what, TunnelNetwork, script)
			}
		}
	}
	if !slices.ContainsFunc(proxyRemovals(), func(taken removal) bool { return taken.kind == KindNetwork && taken.path == TunnelNetwork }) {
		t.Errorf("a teardown leaves the %s network behind", TunnelNetwork)
	}
}
