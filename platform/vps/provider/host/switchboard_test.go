package host

import (
	"context"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func loadsSwitchboard(command string) bool {
	return strings.Contains(command, shell.Words(routing.SwitchboardCommand("load", boxapi.RoutingTablePath)))
}

func reloadsFront(command string) bool {
	return strings.Contains(command, shell.Quote("docker")+" "+shell.Quote("exec")+" "+shell.Quote(caddy.Container)+" "+shell.Quote("caddy")+" "+shell.Quote("reload"))
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

	box := claimingBox(t, routed())
	store := boxapi.Route{RouteKey: boxapi.RouteKey{Owner: surface, Pointer: pointed, App: boxapi.StoreLabel}, Upstream: "shop-storage:9000"}
	if err := box.host().routing.RouteResource(context.Background(), store); err != nil {
		t.Fatalf("RouteResource() = %v", err)
	}
	if loads := box.count(loadsSwitchboard); loads != 1 {
		t.Errorf("a route change loaded the switchboard %d times, want once: it is what routes", loads)
	}
	if reloads := box.count(reloadsFront); reloads != 0 {
		t.Errorf("a route change reloaded %s %d times, want never: a reload drops requests on every hostname the box serves, and no hostname changed", caddy.Container, reloads)
	}
}

func TestAClaimLoadsTheSwitchboardAndLeavesTheFrontProxyRunningAsItWas(t *testing.T) {
	t.Parallel()

	box := claimingBox(t, routed())
	if err := box.host().routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	if loads := box.count(loadsSwitchboard); loads != 1 {
		t.Errorf("a claim loaded the switchboard %d times, want once: it routes the hostname and admits it for a certificate", loads)
	}
	if reloads := box.count(reloadsFront); reloads != 0 {
		t.Errorf("a claim reloaded %s %d times, want never: its config names no hostname, and a reload drops requests on every hostname the box serves (#1280)", caddy.Container, reloads)
	}
}

func TestAPinnedPairLoadsTheSwitchboardBeforeTheFrontProxyReloadsOntoIt(t *testing.T) {
	t.Parallel()

	box, h := pinning(t)
	if err := h.routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	loaded, reloaded := box.at(shell.Words(routing.SwitchboardCommand("load", boxapi.RoutingTablePath))), -1
	for at, command := range box.commands() {
		if reloadsFront(command) {
			reloaded = at
		}
	}
	if loaded < 0 || reloaded < 0 || loaded > reloaded {
		t.Fatalf("the switchboard was loaded at %d and %s reloaded at %d: the pin changes what the front proxy loads, and the switchboard must know the table first", loaded, caddy.Container, reloaded)
	}
	if again := box.count(reloadsFront); again != 1 {
		t.Errorf("a reshape with a new pin reloaded %s %d times, want once", caddy.Container, again)
	}
}

func TestClaimingAHostnameTheBoxAlreadyServesReloadsNothing(t *testing.T) {
	t.Parallel()

	state := routed()
	state.Claims = []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	box := claimingBox(t, state)
	if err := box.host().routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	if loads, reloads := box.count(loadsSwitchboard), box.count(reloadsFront); loads != 0 || reloads != 0 {
		t.Errorf("claiming what the table already contains loaded the switchboard %d times and reloaded %s %d times, want neither", loads, caddy.Container, reloads)
	}
}

func TestAFrontProxyThatRefusesANewPinPutsTheTableAndTheSwitchboardBack(t *testing.T) {
	t.Parallel()

	box, h := pinning(t)
	before := box.recorded
	proxied := box.answer
	box.answer = func(command string) (ssh.Result, bool) {
		if reloadsFront(command) && strings.Contains(box.recorded, claimed) {
			return ssh.Result{Code: 1, Stderr: "loading new config: tls: boom"}, true
		}
		return proxied(command)
	}
	err := h.routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("ClaimHosts() under a front proxy that refused the reload = %v, want its refusal", err)
	}
	if box.recorded != before {
		t.Errorf("the routing table contains\n%s\nafter the front proxy refused it, want what it contained before:\n%s", box.recorded, before)
	}
	if loads := box.count(loadsSwitchboard); loads != 2 {
		t.Errorf("the switchboard was loaded %d times, want twice: onto the claim, then back onto what the table contains again", loads)
	}
}

func TestASwitchboardTheEngineCannotStartIsRefusedWithTheEnginesReason(t *testing.T) {
	t.Parallel()

	const unstarted = `OCI runtime exec failed: exec failed: unable to start container process: exec: "/ocel/switchboard/ocel-switchboard": stat /ocel/switchboard/ocel-switchboard: no such file or directory`
	for _, code := range []int{126, 127} {
		box := claimingBox(t, routed())
		proxied := box.answer
		box.answer = func(command string) (ssh.Result, bool) {
			if loadsSwitchboard(command) {
				return ssh.Result{Code: code, Stdout: unstarted + "\n"}, true
			}
			return proxied(command)
		}
		err := box.host().routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
		if err == nil || !strings.Contains(err.Error(), unstarted) {
			t.Errorf("ClaimHosts() where docker exits %d unable to start the switchboard = %v, want docker's own reason: docker prints it on stdout, and a refusal that says no reason was given leaves the operator nothing to act on", code, err)
		}
	}
}

func TestAProgramThatRanAndFailedNeverHasItsOutputReadAsTheReason(t *testing.T) {
	t.Parallel()

	const printed = "whatever the program printed before it failed"
	box := claimingBox(t, routed())
	proxied := box.answer
	box.answer = func(command string) (ssh.Result, bool) {
		if loadsSwitchboard(command) {
			return ssh.Result{Code: 2, Stdout: printed + "\n"}, true
		}
		return proxied(command)
	}
	err := box.host().routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}})
	if err == nil || strings.Contains(err.Error(), printed) {
		t.Errorf("ClaimHosts() where the switchboard ran and failed = %v, want a refusal that includes none of its stdout: what a program prints is its answer, and a refusal is shown and logged where that answer never is", err)
	}
}

func TestAReleaseGatesCutsOverAndDrainsInsideTheSwitchboardAndNeverReloadsTheFrontProxy(t *testing.T) {
	t.Parallel()

	box, err := rolledOut(t, aRollout(), ssh.Result{}, ssh.Result{}, progress.Discard())
	if err != nil {
		t.Fatalf("Release() = %v", err)
	}
	inside := shell.Words([]string{"docker", "exec", boxapi.Switchboard, boxapi.SwitchboardMounted})
	for _, verb := range []string{"gate", "cutover", "idle"} {
		at := slices.IndexFunc(box.commands(), func(command string) bool {
			asked := inside + " " + shell.Quote(verb)
			watched := shell.Quote(asked)
			return strings.Contains(command, asked) || strings.Contains(command, watched[1:len(watched)-1])
		})
		if at < 0 {
			t.Errorf("the release never ran %s inside %s: %q", verb, boxapi.Switchboard, box.commands())
		}
	}
	if reloads := box.count(reloadsFront); reloads != 0 {
		t.Errorf("a release reloaded %s %d times, want never: a cutover changes no hostname", caddy.Container, reloads)
	}
}

func TestAReleaseOverAConfigTheFrontProxyWasNeverReloadedOntoReloadsItOntoTheOneItWrites(t *testing.T) {
	t.Parallel()

	box := benched(t, ssh.Result{}, ssh.Result{})
	stale := `{"apps":{"http":{"servers":{}}}}`
	proxied := servesPair(box.bench, &box.recorded, &stale)
	answer := box.answer
	box.answer = func(command string) (ssh.Result, bool) {
		if result, mine := proxied(command); mine {
			return result, true
		}
		return answer(command)
	}
	if err := box.host().RollOut(context.Background(), aRollout(), progress.Discard()); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if reloads := box.count(reloadsFront); reloads != 1 {
		t.Errorf("a release that rewrote a config the front proxy does not serve reloaded %s %d times, want once: otherwise it keeps terminating what the file no longer says", caddy.Container, reloads)
	}
	if cutoverAt, reload := box.cutover(), box.after(-1, reloadsFront); cutoverAt < 0 || reload < 0 || reload > cutoverAt {
		t.Errorf("the cutover ran at %d and the front proxy reloaded at %d: the front proxy takes up what was written before the release commits to it, so a refusal leaves the previous release live", cutoverAt, reload)
	}
}

func TestAReleaseWhoseFrontProxyRefusesTheReloadPutsBothFilesBackAndNeverCutsOver(t *testing.T) {
	t.Parallel()

	box := benched(t, ssh.Result{}, ssh.Result{})
	table := box.recorded
	stale := `{"apps":{"http":{"servers":{}}}}`
	config := stale
	proxied := servesPair(box.bench, &box.recorded, &config)
	answer := box.answer
	box.answer = func(command string) (ssh.Result, bool) {
		if reloadsFront(command) {
			return ssh.Result{Code: 1, Stderr: "loading new config: tls: boom"}, true
		}
		if result, mine := proxied(command); mine {
			return result, true
		}
		return answer(command)
	}
	err := box.host().RollOut(context.Background(), aRollout(), progress.Discard())
	if err == nil || !strings.Contains(err.Error(), "boom") || !unserved(err) {
		t.Fatalf("Release() under a front proxy that refused the reload = %v, want its refusal with the release left unserved", err)
	}
	if box.recorded != table || config != stale {
		t.Errorf("a release whose front proxy refused the reload left\n%s\n%s\nwant both files as they were: a refused front-proxy reload puts the box back the same way on every path", box.recorded, config)
	}
	if cutoverAt := box.cutover(); cutoverAt >= 0 {
		t.Errorf("the release cut over at %d after the front proxy refused what it wrote: %v", cutoverAt, box.commands())
	}
	if reloads := box.count(reloadsFront); reloads != 2 {
		t.Errorf("the front proxy was asked to reload %d times, want twice: onto what was written, then back onto what the files contain again, since a refusal whose answer was lost may have loaded it all the same", reloads)
	}
}

func TestTheSwitchboardRunsOnTheBoxNetworkBehindTheFrontProxyAndPublishesNothing(t *testing.T) {
	t.Parallel()

	argv := switchboardBox(nil, Front{}).Run()
	joined := strings.Join(argv, " ")
	for what, wanted := range map[string]string{
		"its name":                         "--name " + boxapi.Switchboard,
		"the box's network":                "--network " + ProxyNetwork,
		"a restart unless stopped":         "--restart unless-stopped",
		"its binary's directory read-only": "--volume " + boxapi.SwitchboardDir + ":" + boxapi.SwitchboardMount + ":ro",
		"the routing table's directory":    "--volume " + boxapi.RoutingDir + ":" + boxapi.RoutingDir + ":ro",
		"the connector's socket directory": "--volume " + ConnectorRun + ":" + ConnectorRun + ":ro",
		"its control socket's directory":   "--volume " + boxapi.SwitchboardControlDir + ":" + boxapi.SwitchboardControlDir,
		"no capability it was not handed":  "--cap-drop ALL",
		"no new privileges":                "--security-opt " + docker.NoNewPrivileges,
		"the front proxy's socket":         "--front " + boxapi.FrontSocket,
		"the front socket's directory":     "--volume " + boxapi.FrontDir + ":" + boxapi.FrontDir,
		"the table it serves":              "--table " + boxapi.RoutingTablePath,
	} {
		if !strings.Contains(joined, wanted) {
			t.Errorf("the switchboard runs as %q, which contains no %s (%s)", joined, what, wanted)
		}
	}
	if path.Dir(boxapi.ConnectorSocket) != ConnectorRun {
		t.Errorf("the switchboard dials %s, outside the %s it is handed", boxapi.ConnectorSocket, ConnectorRun)
	}
	if strings.Contains(joined, "--publish") {
		t.Errorf("the switchboard publishes a port: %q; only the front proxy is reached from outside the box", joined)
	}
	if at := slices.Index(argv, docker.StaticImage); at < 0 || !strings.Contains(docker.StaticImage, "@sha256:") {
		t.Errorf("the switchboard runs %q, want an image pinned by digest", docker.StaticImage)
	}
	for _, capability := range []string{"NET_BIND_SERVICE", "NET_ADMIN", "SYS_ADMIN"} {
		if slices.Contains(argv, capability) {
			t.Errorf("the switchboard is handed %s, which nothing it does needs", capability)
		}
	}
}

func TestTheFrontProxyMountsNoRoutingStateTheSwitchboardNowOwns(t *testing.T) {
	t.Parallel()

	joined := strings.Join(frontProxy().Run(), " ")
	for _, owned := range []string{ConnectorRun, boxapi.RoutingDir, boxapi.SwitchboardControlDir} {
		if strings.Contains(joined, "--volume "+owned+":") {
			t.Errorf("the front proxy runs as %q, which still reaches %s", joined, owned)
		}
	}
	if reader := "--volume " + boxapi.SwitchboardDir + ":" + boxapi.SwitchboardMount + ":ro"; !strings.Contains(joined, reader) {
		t.Errorf("the front proxy runs as %q, which contains no %s: its image has nothing that reads its admin socket, so nothing proves the socket answers", joined, reader)
	}
	if !slices.Equal(frontProxy().Run()[len(frontProxy().Run())-len(caddy.Command()):], caddy.Command()) {
		t.Errorf("the front proxy runs %q, want caddy started straight off its config", frontProxy().Run())
	}
}

func TestEveryProjectNetworkJoinsTheSwitchboardAndNeverTheFrontProxy(t *testing.T) {
	t.Parallel()

	for what, script := range map[string]string{
		"joining a network":    docker.JoiningNetwork(environment.TierProduction, "shop"),
		"forgetting a network": docker.ForgettingNetwork(environment.TierProduction, "shop"),
		"recreating the board": switchboardBox(nil, Front{}).Writing(1),
	} {
		if !strings.Contains(script, shell.Quote(boxapi.Switchboard)) {
			t.Errorf("%s runs\n%s\nwhich never names %s: it reaches every upstream", what, script, boxapi.Switchboard)
		}
		for line := range strings.Lines(script) {
			if strings.Contains(line, "docker network") && strings.Contains(line, shell.Quote(caddy.Container)) {
				t.Errorf("%s runs %q, which puts %s on a project's network: the front proxy reaches the switchboard alone", what, line, caddy.Container)
			}
		}
	}
}

func TestTheBoxProbesWhatItServesOnItsOwnHttpsPortWithoutDocker(t *testing.T) {
	t.Parallel()

	for verb, probe := range map[string]func(h *Host) error{
		"probe": func(h *Host) error { _, err := h.ProbeRouter(context.Background(), "web.localhost"); return err },
		"leaf": func(h *Host) error {
			_, err := h.ReadServedCertificate(context.Background(), "web.localhost")
			return err
		},
	} {
		box := machine(nil)
		if err := probe(box.host()); err != nil {
			t.Fatalf("%s = %v", verb, err)
		}
		want := shell.Words([]string{boxapi.SwitchboardBinary, verb, "web.localhost"})
		if at := box.at(want); at < 0 {
			t.Errorf("%s ran %q, want %s run on the box itself", verb, box.commands(), want)
		}
		for _, command := range box.commands() {
			if strings.Contains(command, "docker") {
				t.Errorf("%s ran %q, and a probe of what the box serves on :443 needs no container", verb, command)
			}
		}
	}
}

func TestTheDeployLoginIsToldItRunsTheSwitchboardAndCannotWriteIt(t *testing.T) {
	t.Parallel()

	at := slices.IndexFunc(grants(environment.TierProduction, ssh.AMD64, Front{}), func(grant Grant) bool {
		return grant.Name == "runs "+boxapi.SwitchboardBinary
	})
	if at < 0 {
		t.Fatalf("the deploy grants never name %s, and the deploy login runs it on the box for every loopback probe", boxapi.SwitchboardBinary)
	}
	if detail := grants(environment.TierProduction, ssh.AMD64, Front{})[at].Detail; !strings.Contains(detail, "0755") || !strings.Contains(detail, "cannot write it") {
		t.Errorf("the grant reads %q, want the mode it is written at and that the login cannot write it", detail)
	}
}

func TestAProbeTheBoxCannotAnswerYetFailsWithAOneLineReason(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, shell.Quote("probe")) {
			return ssh.Result{Code: 3, Stderr: "web.localhost at 127.0.0.1:443: the certificate served is for fallback.localhost, not this name\n"}, true
		}
		return ssh.Result{}, false
	}
	said, err := box.host().ProbeRouter(context.Background(), "web.localhost")
	if err != nil || said.Failure == "" || strings.Contains(said.Failure, "\n") {
		t.Errorf("ProbeRouter() over a box not serving yet = %+v, %v, want its failure on one line", said, err)
	}
	box.answer = func(string) (ssh.Result, bool) { return ssh.Result{Code: 2, Stderr: "usage"}, true }
	if _, err := box.host().ProbeRouter(context.Background(), "web.localhost"); err == nil {
		t.Error("ProbeRouter() over a probe that refused = nil, want the refusal")
	}
}

func pruneAnswers(sum string) func(command string) (ssh.Result, bool) {
	return func(command string) (ssh.Result, bool) {
		switch {
		case strings.Contains(command, shell.Words(switchboardBox(nil, Front{}).Readiness())) && !strings.Contains(command, "while :"):
			return ssh.Result{Code: 1, Stderr: "Error: No such container: " + boxapi.Switchboard}, true
		case command == docker.StateCommand(boxapi.Switchboard):
			return ssh.Result{Stdout: "Error: No such object: " + boxapi.Switchboard}, true
		case strings.Contains(command, "missing="):
			return ssh.Result{Stdout: "sum=" + sum + "\n"}, true
		}
		return ssh.Result{}, false
	}
}

func TestASwitchboardRestoredIsTheOneBootstrapRunsWithTheBinaryTheBoxHas(t *testing.T) {
	t.Parallel()

	const sum = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
	for what, front := range map[string]Front{
		"ocel's own proxy":           {},
		"a proxy you route to":       {Manual: &ManualFront{Port: 8480}},
		"a proxy on another port":    {Manual: &ManualFront{Port: 9000}},
		"a proxy on its own network": routedOnANetwork(),
	} {
		b := machine(nil)
		b.answer = pruneAnswers(sum)
		if err := b.fronted(front).CheckProxy(context.Background()); err != nil {
			t.Fatalf("%s: CheckProxy() = %v, want the switchboard restored", what, err)
		}
		bootstrapped := switchboardBox(switchboardBinary(ssh.AMD64), front)
		want := slices.Clone(bootstrapped.Create())
		labelled := slices.Index(want, docker.ConfigLabel+"="+bootstrapped.Config)
		want[labelled] = docker.ConfigLabel + "=" + sum
		want = slices.Insert(want, labelled+1, "--label", docker.RestoredLabel+"="+docker.RestoredBy)
		if b.at(shell.Words(want)) < 0 {
			t.Errorf("%s: no command started the switchboard bootstrap runs, labelled with the binary the box has and as restored:\n%s\nran:\n%s",
				what, shell.Words(want), strings.Join(b.commands(), "\n---\n"))
		}
	}
}

func TestASwitchboardAPruneLeftStoppedWithItsNetworkGoneIsRecreatedInPlaceOfTheStoppedOne(t *testing.T) {
	t.Parallel()

	b := machine(nil)
	pruned := pruneAnswers("0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0")
	b.answer = func(command string) (ssh.Result, bool) {
		if command == docker.StateCommand(boxapi.Switchboard) {
			return ssh.Result{Stdout: "Status=exited ExitCode=0 OOMKilled=false Error= RestartCount=0\n"}, true
		}
		return pruned(command)
	}
	if err := b.fronted(coolifysTraefik()).CheckProxy(context.Background()); err != nil {
		t.Fatalf("CheckProxy() = %v, want the switchboard recreated: Coolify prunes the ocel network of a stopped switchboard and leaves the container, which docker start can then never start", err)
	}
	at := b.at(shell.Quote("docker") + " " + shell.Quote("create"))
	if at < 0 {
		t.Fatalf("no command started the switchboard again:\n%s", strings.Join(b.commands(), "\n---\n"))
	}
	command := b.commands()[at]
	removed := strings.Index(command, "docker rm --force "+shell.Quote(boxapi.Switchboard))
	if removed < 0 || removed > strings.Index(command, shell.Quote("docker")+" "+shell.Quote("create")) {
		t.Errorf("the switchboard was recreated as\n%s\nwant the stopped one removed first, and only while it is not running", command)
	}
}

func TestASwitchboardRestoredOnYourProxysNetworkAsksAfterItBeforeRunning(t *testing.T) {
	t.Parallel()

	b := machine(nil)
	b.answer = pruneAnswers("0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0")
	if err := b.fronted(routedOnANetwork()).CheckProxy(context.Background()); err != nil {
		t.Fatalf("CheckProxy() = %v, want the switchboard restored", err)
	}
	command := b.commands()[b.at(shell.Quote("docker")+" "+shell.Quote("create"))]
	asked := strings.Index(command, "docker network inspect "+shell.Quote("coolify"))
	ran := strings.Index(command, shell.Quote("docker")+" "+shell.Quote("create"))
	if asked < 0 || asked > ran {
		t.Fatalf("the switchboard was restored asking after coolify at %d and running at %d, want the network found before a run docker would refuse without naming the option:\n%s", asked, ran, command)
	}
	if !strings.Contains(command, "proxy.manual.network") {
		t.Errorf("the restored switchboard refuses a missing network without naming the option that set it:\n%s", command)
	}
}
