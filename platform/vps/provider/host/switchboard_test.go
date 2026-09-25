package host

import (
	"context"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func loadsSwitchboard(command string) bool {
	return strings.Contains(command, words(switchboardCommand("load", live.RoutingTable)))
}

func reloadsFront(command string) bool {
	return strings.Contains(command, quoted("docker")+" "+quoted("exec")+" "+quoted(caddy.Container)+" "+quoted("caddy")+" "+quoted("reload"))
}

func (b *bench) count(matches func(string) bool) int {
	counted := 0
	for _, command := range b.commands() {
		if matches(command) {
			counted++
		}
	}
	return counted
}

func TestARouteChangeLoadsTheSwitchboardAndLeavesTheFrontProxyRunningAsItWas(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	store := AppRoute{RouteKey: RouteKey{Owner: surface, Pointer: pointed, App: switchboard.StoreLabel}, Upstream: "shop-storage:9000"}
	if err := stood.host().RouteResource(context.Background(), store); err != nil {
		t.Fatalf("RouteResource() = %v", err)
	}
	if loads := stood.count(loadsSwitchboard); loads != 1 {
		t.Errorf("a route change loaded the switchboard %d times, want once: it is what routes", loads)
	}
	if reloads := stood.count(reloadsFront); reloads != 0 {
		t.Errorf("a route change reloaded %s %d times, want never: a reload drops requests on every hostname the box serves, and no hostname changed", caddy.Container, reloads)
	}
}

func TestAClaimLoadsTheSwitchboardBeforeTheFrontProxyTakesTheHostname(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	if err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	loaded, reloaded := stood.at(words(switchboardCommand("load", live.RoutingTable))), -1
	for at, command := range stood.commands() {
		if reloadsFront(command) {
			reloaded = at
		}
	}
	if loaded < 0 || reloaded < 0 || loaded > reloaded {
		t.Fatalf("the switchboard was loaded at %d and %s reloaded at %d: a hostname the front proxy forwards before the switchboard knows it answers the box's 404", loaded, caddy.Container, reloaded)
	}
	if again := stood.count(reloadsFront); again != 1 {
		t.Errorf("a claim reloaded %s %d times, want once", caddy.Container, again)
	}
}

func TestClaimingAHostnameTheBoxAlreadyServesReloadsNothing(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood := claimingBox(t, state)
	if err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	if loads, reloads := stood.count(loadsSwitchboard), stood.count(reloadsFront); loads != 0 || reloads != 0 {
		t.Errorf("claiming what the table already holds loaded the switchboard %d times and reloaded %s %d times, want neither", loads, caddy.Container, reloads)
	}
}

func TestAFrontProxyThatRefusesTheNewHostnameSetPutsTheTableAndTheSwitchboardBack(t *testing.T) {
	t.Parallel()

	stood := claimingBox(t, routed())
	before := stood.held
	proxied := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if reloadsFront(command) && strings.Contains(stood.held, claimed) {
			return session.Result{Code: 1, Stderr: "loading new config: tls: boom"}, true
		}
		return proxied(command)
	}
	err := stood.host().ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("ClaimHosts() under a front proxy that refused the reload = %v, want its refusal", err)
	}
	if stood.held != before {
		t.Errorf("the routing table holds\n%s\nafter the front proxy refused it, want what it held before:\n%s", stood.held, before)
	}
	if loads := stood.count(loadsSwitchboard); loads != 2 {
		t.Errorf("the switchboard was loaded %d times, want twice: onto the claim, then back onto what the table holds again", loads)
	}
}

func TestAReleaseGatesFlipsAndDrainsInsideTheSwitchboardAndNeverReloadsTheFrontProxy(t *testing.T) {
	t.Parallel()

	stood, err := released(t, aRelease(), session.Result{}, session.Result{}, nil)
	if err != nil {
		t.Fatalf("Release() = %v", err)
	}
	inside := words([]string{"docker", "exec", SwitchboardContainer, SwitchboardMounted})
	for _, verb := range []string{"gate", "flip", "idle"} {
		at := slices.IndexFunc(stood.commands(), func(command string) bool {
			return strings.Contains(command, inside+" "+quoted(verb))
		})
		if at < 0 {
			t.Errorf("the release never ran %s inside %s: %q", verb, SwitchboardContainer, stood.commands())
		}
	}
	if reloads := stood.count(reloadsFront); reloads != 0 {
		t.Errorf("a release reloaded %s %d times, want never: a flip changes no hostname", caddy.Container, reloads)
	}
}

func TestAReleaseOverAConfigTheFrontProxyWasNeverReloadedOntoReloadsItOntoTheOneItWrites(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{}, session.Result{})
	stale := `{"apps":{"http":{"servers":{}}}}`
	proxied := servesPair(stood.bench, &stood.held, &stale)
	answer := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if result, mine := proxied(command); mine {
			return result, true
		}
		return answer(command)
	}
	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if reloads := stood.count(reloadsFront); reloads != 1 {
		t.Errorf("a release that rewrote a config the front proxy does not serve reloaded %s %d times, want once: otherwise it keeps terminating what the file no longer says", caddy.Container, reloads)
	}
	if flip, reload := stood.cutover(), stood.after(-1, reloadsFront); flip < 0 || reload < flip {
		t.Errorf("the flip ran at %d and the front proxy reloaded at %d: the release moves the route first and the front proxy follows", flip, reload)
	}
}

func TestAReleaseWhoseFrontProxyRefusedTheReloadLeavesItsConfigAsTheProxyServesIt(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{}, session.Result{})
	stale := `{"apps":{"http":{"servers":{}}}}`
	config := stale
	proxied := servesPair(stood.bench, &stood.held, &config)
	answer := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if reloadsFront(command) {
			return session.Result{Code: 1, Stderr: "the connection dropped before the reload answered"}, true
		}
		if result, mine := proxied(command); mine {
			return result, true
		}
		return answer(command)
	}
	err := stood.host().Release(context.Background(), aRelease(), nil)
	if err == nil || !strings.Contains(err.Error(), "not reloaded") {
		t.Fatalf("Release() under a front proxy that refused the reload = %v, want the refusal carried out", err)
	}
	if config != stale {
		t.Errorf("a release whose reload failed left the front proxy's config as\n%s\nwant it put back to\n%s\nas the proxy still serves it: every later write compares the table's render with the file, finds them equal, and never reloads the proxy again", config, stale)
	}
	if got := upstreamsOf(stood.state(t)); got["web"] != flipTo {
		t.Errorf("the table records %v after the flip, want the release it flipped onto: only the front proxy's config is put back", got)
	}
	again := stood.count(reloadsFront)
	if err := stood.host().rerender(context.Background()); err == nil {
		t.Fatal("rerender() under a front proxy still refusing = nil, want its refusal")
	}
	if stood.count(reloadsFront) == again {
		t.Error("the next write after a reload that failed never asked the front proxy to reload, so it serves the old config for good")
	}
}

func TestTheSwitchboardRunsOnTheBoxNetworkBehindTheFrontProxyAndPublishesNothing(t *testing.T) {
	t.Parallel()

	argv := switchboardRun()
	joined := strings.Join(argv, " ")
	for what, wanted := range map[string]string{
		"its name":                         "--name " + SwitchboardContainer,
		"the box's network":                "--network " + ProxyNetwork,
		"a restart unless stopped":         "--restart unless-stopped",
		"its binary's directory read-only": "--volume " + SwitchboardDir + ":" + switchboardMount + ":ro",
		"the routing table's directory":    "--volume " + live.RoutingDir + ":" + live.RoutingDir + ":ro",
		"the connector's socket directory": "--volume " + ConnectorRun + ":" + ConnectorRun + ":ro",
		"its control socket's directory":   "--volume " + switchboard.ControlDir + ":" + switchboard.ControlDir,
		"no capability it was not handed":  "--cap-drop ALL",
		"no new privileges":                "--security-opt " + noNewPrivileges,
		"trust in the front proxy by name": "--trust " + caddy.Container,
		"the table it serves":              "--table " + live.RoutingTable,
	} {
		if !strings.Contains(joined, wanted) {
			t.Errorf("the switchboard runs as %q, which carries no %s (%s)", joined, what, wanted)
		}
	}
	if path.Dir(switchboard.ConnectorSocket) != ConnectorRun {
		t.Errorf("the switchboard dials %s, outside the %s it is handed", switchboard.ConnectorSocket, ConnectorRun)
	}
	if strings.Contains(joined, "--publish") {
		t.Errorf("the switchboard publishes a port: %q; only the front proxy is reached from outside the box", joined)
	}
	if at := slices.Index(argv, SwitchboardImage); at < 0 || !strings.Contains(SwitchboardImage, "@sha256:") {
		t.Errorf("the switchboard runs %q, want an image pinned by digest", SwitchboardImage)
	}
	for _, capability := range []string{"NET_BIND_SERVICE", "NET_ADMIN", "SYS_ADMIN"} {
		if slices.Contains(argv, capability) {
			t.Errorf("the switchboard is handed %s, which nothing it does needs", capability)
		}
	}
}

func TestTheFrontProxyMountsNothingTheSwitchboardNowOwns(t *testing.T) {
	t.Parallel()

	joined := strings.Join(proxyRun(), " ")
	for _, owned := range []string{ConnectorRun, live.RoutingDir, SwitchboardDir} {
		if strings.Contains(joined, owned) {
			t.Errorf("the front proxy runs as %q, which still reaches %s", joined, owned)
		}
	}
	if !slices.Equal(proxyRun()[len(proxyRun())-len(caddy.Command()):], caddy.Command()) {
		t.Errorf("the front proxy runs %q, want caddy started straight off its config", proxyRun())
	}
}

func TestEveryProjectNetworkJoinsTheSwitchboardAndNeverTheFrontProxy(t *testing.T) {
	t.Parallel()

	for what, script := range map[string]string{
		"standing a network":   networkStanding(providerkit.ClassProduction, "shop"),
		"forgetting a network": networkForgetting(providerkit.ClassProduction, "shop"),
		"recreating the board": switchboardWriting(1),
	} {
		if !strings.Contains(script, quoted(SwitchboardContainer)) {
			t.Errorf("%s runs\n%s\nwhich never names %s: it reaches every upstream", what, script, SwitchboardContainer)
		}
		for line := range strings.Lines(script) {
			if strings.Contains(line, "docker network") && strings.Contains(line, quoted(caddy.Container)) {
				t.Errorf("%s runs %q, which puts %s on a project's network: the front proxy reaches the switchboard alone", what, line, caddy.Container)
			}
		}
	}
}

func TestTheBoxProbesWhatItServesOnItsOwnHttpsPortWithoutDocker(t *testing.T) {
	t.Parallel()

	for verb, probe := range map[string]func(h *Host) error{
		"probe": func(h *Host) error { _, err := h.ServedEdge(context.Background(), "web.localhost"); return err },
		"leaf":  func(h *Host) error { _, err := h.ServedCertificate(context.Background(), "web.localhost"); return err },
	} {
		stood := machine(nil)
		if err := probe(stood.host()); err != nil {
			t.Fatalf("%s = %v", verb, err)
		}
		want := words([]string{SwitchboardBinary, verb, "web.localhost"})
		if at := stood.at(want); at < 0 {
			t.Errorf("%s ran %q, want %s run on the box itself", verb, stood.commands(), want)
		}
		for _, command := range stood.commands() {
			if strings.Contains(command, "docker") {
				t.Errorf("%s ran %q, and a probe of what the box serves on :443 needs no container", verb, command)
			}
		}
	}
}

func TestTheDeployLoginIsToldItRunsTheSwitchboardAndCannotWriteIt(t *testing.T) {
	t.Parallel()

	at := slices.IndexFunc(grants(providerkit.ClassProduction, ArchAMD64), func(grant Grant) bool {
		return grant.Name == "runs "+SwitchboardBinary
	})
	if at < 0 {
		t.Fatalf("the deploy grants never name %s, and the deploy login runs it on the box for every loopback probe", SwitchboardBinary)
	}
	if detail := grants(providerkit.ClassProduction, ArchAMD64)[at].Detail; !strings.Contains(detail, "0755") || !strings.Contains(detail, "cannot write it") {
		t.Errorf("the grant reads %q, want the mode it is written at and that the login cannot write it", detail)
	}
}

func TestAProbeTheBoxCannotAnswerYetIsUnreachedAndItsReasonIsOneLine(t *testing.T) {
	t.Parallel()

	stood := machine(nil)
	stood.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, quoted("probe")) {
			return session.Result{Code: 3, Stderr: "web.localhost at 127.0.0.1:443: the certificate served is for fallback.localhost, not this name\n"}, true
		}
		return session.Result{}, false
	}
	said, err := stood.host().ServedEdge(context.Background(), "web.localhost")
	if err != nil || said.Unreached == "" || strings.Contains(said.Unreached, "\n") {
		t.Errorf("ServedEdge() over a box not serving yet = %+v, %v, want one unreached line", said, err)
	}
	stood.answer = func(string) (session.Result, bool) { return session.Result{Code: 2, Stderr: "usage"}, true }
	if _, err := stood.host().ServedEdge(context.Background(), "web.localhost"); err == nil {
		t.Error("ServedEdge() over a probe that refused = nil, want the refusal")
	}
}
