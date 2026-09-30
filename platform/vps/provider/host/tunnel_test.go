package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
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
	for _, want := range []string{quoted(TunnelContainer), quoted(tunnelConnectors[tunnelEdge].image), quoted("--no-autoupdate"), quoted("TUNNEL_TOKEN_FILE=" + tunnelTokenMounted(opened.Name)), quoted("--network") + " " + quoted(TunnelNetwork)} {
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
	if written < 0 || !strings.Contains(box.commands()[written], words(renderTunnelTokenArgv("place-secret", opened.Name))) {
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

	if !isBusy(err) || !errors.Is(err, ErrTunnelReleased) || box.at(quoted("docker")+" "+quoted("run")) >= 0 {
		t.Errorf("RunTunnel() under a name the box no longer reserves = %v, running %v, want it refused busy as a released tunnel with nothing run: that tunnel is being deleted", err, box.commands())
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
	if box.at("docker rm --force "+quoted(TunnelContainer)) < 0 || box.at(words(renderTunnelTokenArgv("unplace", reserved.Name))) < 0 {
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

type tunnelShell struct {
	dir, bin, labelled, removed, unplaced string
}

func aTunnelShell(t *testing.T, runningID string) *tunnelShell {
	t.Helper()
	dir := t.TempDir()
	shell := &tunnelShell{
		dir:      dir,
		bin:      filepath.Join(dir, "bin"),
		labelled: filepath.Join(dir, "labelled"),
		removed:  filepath.Join(dir, "removed"),
		unplaced: filepath.Join(dir, "unplaced"),
	}
	if err := os.Mkdir(shell.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shell.labelled, []byte(runningID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable(t, filepath.Join(shell.bin, "docker"), "#!/bin/sh\n"+
		"for last; do :; done\n"+
		"case \"$1\" in\n"+
		"inspect) if [ -f "+quoted(shell.labelled)+" ]; then cat "+quoted(shell.labelled)+"; else exit 1; fi;;\n"+
		"rm) printf '%s\\n' \"$last\" >> "+quoted(shell.removed)+"; rm -f "+quoted(shell.labelled)+";;\n"+
		"run) case \"$*\" in *unplace*) printf '%s\\n' \"$last\" >> "+quoted(shell.unplaced)+";; esac;;\n"+
		"esac\n")
	return shell
}

func (s *tunnelShell) run(t *testing.T, script string) {
	t.Helper()
	command := exec.Command("/bin/sh", "-c", strings.NewReplacer(routingLock, s.dir).Replace(script))
	command.Env = append(os.Environ(), "PATH="+s.bin+":"+os.Getenv("PATH"))
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("the script = %v\n%s\n%s", err, out, script)
	}
}

func (s *tunnelShell) read(path string) string {
	read, _ := os.ReadFile(path)
	return strings.TrimSpace(string(read))
}

func tokenPathOf(t *testing.T, tunnel Tunnel) string {
	t.Helper()
	connector := tunnelConnectors[tunnel.Edge]
	for _, set := range tunnelBox(tunnel, connector).env {
		if path, found := strings.CutPrefix(set, connector.tokenFileEnv+"="); found {
			return path
		}
	}
	t.Fatalf("the tunnel %s runs reading no token file", tunnel.Name)
	return ""
}

func TestReleasingATunnelLeavesTheReplacementAnotherClaimStartedAfterIt(t *testing.T) {
	t.Parallel()
	box, retired := tunneledBox(t)
	if err := box.host().RemoveTunneledHost(context.Background(), TunneledHost{Hostname: claimed, Owner: surface}); err != nil {
		t.Fatal(err)
	}
	if _, err := box.host().ReleaseTunnel(context.Background()); err != nil {
		t.Fatalf("ReleaseTunnel() = %v", err)
	}
	removal := box.at("unplace")
	if removal < 0 {
		t.Fatalf("the released tunnel's token was never removed: %v", box.commands())
	}
	replacement := Tunnel{Edge: tunnelEdge, Name: "ocel-203-0-113-10-ffffffff", ID: "9e8d7c6b-2", Address: "9e8d7c6b-2.cfargotunnel.com"}
	shell := aTunnelShell(t, replacement.ID)

	shell.run(t, box.commands()[removal])

	if removed := shell.read(shell.removed); removed != "" {
		t.Errorf("releasing %s removed %s while it ran the replacement %s: the replacement's claim is left with no tunnel", retired.ID, removed, replacement.ID)
	}
	if unplaced := shell.read(shell.unplaced); unplaced != tokenPathOf(t, retired) || unplaced == tokenPathOf(t, replacement) {
		t.Errorf("releasing %s removed the token at %q, want only its own at %s and never the replacement's at %s", retired.Name, unplaced, tokenPathOf(t, retired), tokenPathOf(t, replacement))
	}

	shell = aTunnelShell(t, retired.ID)
	shell.run(t, box.commands()[removal])
	if removed := shell.read(shell.removed); removed != TunnelContainer {
		t.Errorf("releasing %s while its own container still ran removed %q, want %s", retired.ID, removed, TunnelContainer)
	}
}

func TestATunnelReleasedAndReplacedWhileItsRunPausedNeverReplacesTheNewConnector(t *testing.T) {
	t.Parallel()
	box, paused := tunneledBox(t)
	paused.ID, paused.Address = "5a6b7c8d-2", "5a6b7c8d-2.cfargotunnel.com"
	replacement := Tunnel{Edge: tunnelEdge, Name: "ocel-203-0-113-10-ffffffff", ID: "9e8d7c6b-2", Address: "9e8d7c6b-2.cfargotunnel.com"}
	serves := box.answer
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, words(renderTunnelTokenArgv("place-secret", paused.Name))) {
			state := readBack(t, box)
			state.Retired, state.Tunnel = append(state.Retired, *state.Tunnel), &replacement
			box.recorded = string(mustWrite(t, state))
		}
		return serves(command)
	}

	err := box.host().RunTunnel(context.Background(), paused, tokenOf("the-paused-token"))

	if !isBusy(err) || !errors.Is(err, ErrTunnelReleased) {
		t.Errorf("RunTunnel() once another run released and replaced its tunnel = %v, want it refused busy as released", err)
	}
	if started := box.at(quoted("--name") + " " + quoted(TunnelContainer)); started >= 0 {
		t.Errorf("the paused run started its connector with\n%s\nover the replacement %s the box now reserves", box.commands()[started], replacement.Name)
	}
	if box.at(words(renderTunnelTokenArgv("unplace", replacement.Name))) >= 0 || box.at(words(renderTunnelTokenArgv("unplace", paused.Name))) < 0 {
		t.Errorf("the paused run left its own token or removed the replacement's: %v", box.commands())
	}
}

func TestATunnelStartsOnlyWhileTheTableItCheckedIsTheOneOnTheBox(t *testing.T) {
	t.Parallel()
	box, reserved := tunneledBox(t)
	opened := reserved
	opened.ID = "9e8d7c6b-2"
	if err := box.host().RunTunnel(context.Background(), opened, tokenOf("the-tunnel-token")); err != nil {
		t.Fatalf("RunTunnel() = %v", err)
	}
	start := box.at(quoted("--name") + " " + quoted(TunnelContainer))
	if start < 0 {
		t.Fatalf("the tunnel was never started: %v", box.commands())
	}
	shell := aTunnelShell(t, reserved.ID)
	moved := filepath.Join(shell.dir, "routing-table.json")
	if err := os.WriteFile(moved, []byte("a table another run wrote since\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", "-c", strings.NewReplacer(routingLock, shell.dir, live.RoutingTable, moved).Replace(box.commands()[start]))
	command.Env = append(os.Environ(), "PATH="+shell.bin+":"+os.Getenv("PATH"))
	out, _ := command.CombinedOutput()

	if code := command.ProcessState.ExitCode(); code != routingMoved {
		t.Errorf("the start under a table that moved exited %d, want %d so the run checks the reservation again:\n%s", code, routingMoved, out)
	}
	if removed := shell.read(shell.removed); removed != "" {
		t.Errorf("the start under a table that moved removed %s before it knew the box still reserved %s", removed, opened.Name)
	}
}

func TestATunnelIsStartedAndRemovedOnlyUnderTheLockEveryRoutingWriterTakes(t *testing.T) {
	t.Parallel()
	box, reserved := tunneledBox(t)
	opened := reserved
	opened.ID = "9e8d7c6b-2"
	if err := box.host().RunTunnel(context.Background(), opened, tokenOf("the-tunnel-token")); err != nil {
		t.Fatalf("RunTunnel() = %v", err)
	}
	if err := box.host().RemoveTunneledHost(context.Background(), TunneledHost{Hostname: claimed, Owner: surface}); err != nil {
		t.Fatal(err)
	}
	if _, err := box.host().ReleaseTunnel(context.Background()); err != nil {
		t.Fatalf("ReleaseTunnel() = %v", err)
	}

	for what, at := range map[string]int{"started": box.at(quoted("--name") + " " + quoted(TunnelContainer)), "removed": box.at("unplace")} {
		if at < 0 {
			t.Fatalf("the tunnel was never %s: %v", what, box.commands())
		}
		command := box.commands()[at]
		locked := strings.Index(command, "exec 9<"+quoted(routingLock)+"\nflock -x 9")
		if locked < 0 || strings.Index(command, "docker rm") < locked {
			t.Errorf("the tunnel is %s by\n%s\nwhich does not hold %s first, so a release and a claim's start interleave and one removes the other's container", what, command, routingLock)
		}
	}
}
