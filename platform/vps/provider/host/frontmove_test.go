package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func claimedThrice(t *testing.T) string {
	t.Helper()
	state := routed()
	state.Claims = []HostClaim{
		{Hostname: claimed, Owner: surface, Pointer: pointed},
		{Hostname: "www." + claimed, Owner: surface, Pointer: pointed},
	}
	state.Connector = "box.example.com"
	return string(mustWrite(t, state))
}

func boxRecordedFor(t *testing.T, tier environment.Tier, recorded Front) *bench {
	t.Helper()
	table := claimedThrice(t)
	box := bootstrappedBehind(t, tier, recorded, &table, nil)
	recordOn(t, box, tier, recorded, "blog")
	return box
}

func traefikIn(directory string) Front {
	moved := *traefikOnTheHost().Traefik
	moved.Directory = directory
	return Front{Traefik: &moved}
}

func movePlanned(t *testing.T, box *bench, tier environment.Tier, to Front) provider.ChangeGroup {
	t.Helper()
	plan, err := NewBootstrap(box.fronted(to), testVendor, "shop").Plan(context.Background(), provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() = %v, want the move planned", err)
	}
	for _, group := range plan.Groups {
		if group.Kind == MoveGroupKind {
			return group
		}
	}
	t.Fatalf("the plan %+v has no %s group, want the move shown as one change group", plan.Groups, MoveGroupKind)
	return provider.ChangeGroup{}
}

func changeIn(group provider.ChangeGroup, name string) (provider.Change, bool) {
	for _, change := range group.Changes {
		if change.Name == name {
			return change, true
		}
	}
	return provider.Change{}, false
}

func wantChange(t *testing.T, group provider.ChangeGroup, name string, action provider.ChangeAction) provider.Change {
	t.Helper()
	change, ok := changeIn(group, name)
	if !ok {
		t.Errorf("the move %q lists no %s among %+v", group.Name, name, group.Changes)
		return change
	}
	if change.Action != action {
		t.Errorf("the move %q would %s %s, want %s", group.Name, change.Action, name, action)
	}
	return change
}

func TestMovingABoxFromOcelsOwnProxyToYoursPlansTakingOcelsProxyAwayAndTheOutageUntilYoursServes(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	move := movePlanned(t, boxRecordedFor(t, tier, Front{}), tier, traefikOnTheHost())
	if want := string(testVendor) + "/ocel's own proxy → your Traefik"; move.Name != want {
		t.Errorf("the move is named %q, want %q", move.Name, want)
	}
	if move.Action != provider.ActionReplace {
		t.Errorf("the move acts %q, want %q", move.Action, provider.ActionReplace)
	}
	if want := "outage from " + caddy.Container + " stopping until your Traefik holds 443 and has its certificates"; move.Reason != want {
		t.Errorf("the move gives its outage as %q, want %q", move.Reason, want)
	}
	wantChange(t, move, caddy.Container, provider.ActionDelete)
	wantChange(t, move, ProxyData, provider.ActionDelete)
	if pins := wantChange(t, move, caddy.PinsDir, provider.ActionDelete); !strings.Contains(pins.Reason, "only if empty") {
		t.Errorf("the move takes %s as %q, want it kept while it holds your pinned certificates", caddy.PinsDir, pins.Reason)
	}
	wantChange(t, move, "/etc/traefik/dynamic/ocel.yml", provider.ActionCreate)
	wantChange(t, move, FrontRecordPath, provider.ActionUpdate)
	wantChange(t, move, "3 hostnames get new certificates from your Traefik", provider.ActionCreate)
}

func TestMovingABoxFromYourProxyToOcelsOwnPlansTakingOcelsFileOutAndTheOutageUntilOcelsProxyAnswers(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	move := movePlanned(t, boxRecordedFor(t, tier, traefikOnTheHost()), tier, Front{})
	if want := string(testVendor) + "/your Traefik → ocel's own proxy"; move.Name != want {
		t.Errorf("the move is named %q, want %q", move.Name, want)
	}
	if want := "outage from your Traefik stopping until " + caddy.Container + " answers"; move.Reason != want {
		t.Errorf("the move gives its outage as %q, want %q", move.Reason, want)
	}
	wantChange(t, move, "/etc/traefik/dynamic/ocel.yml", provider.ActionDelete)
	wantChange(t, move, caddy.Container, provider.ActionCreate)
	wantChange(t, move, "3 hostnames get new certificates from ocel's own proxy", provider.ActionCreate)
}

func TestMovingABoxWithinYourOwnProxyPlansTheNewFileBeforeTheOldAndNoOutage(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := boxRecordedFor(t, tier, traefikOnTheHost())
	portsOwnedOn(box, nil, socketOwner{80, "traefik"}, socketOwner{443, "traefik"})
	move := movePlanned(t, box, tier, traefikIn("/etc/traefik/dynamic/ocel"))
	if want := "no outage: your Traefik serves throughout"; move.Reason != want {
		t.Errorf("the move gives its outage as %q, want %q", move.Reason, want)
	}
	wantChange(t, move, "/etc/traefik/dynamic/ocel/ocel.yml", provider.ActionCreate)
	wantChange(t, move, "/etc/traefik/dynamic/ocel.yml", provider.ActionDelete)
	for _, change := range move.Changes {
		if change.Kind == KindCertificates {
			t.Errorf("the move lists %q, want no certificate ordered again: the same Traefik keeps the ones its resolver holds", change.Name)
		}
	}
}

func TestMovingABoxWithinYourOwnTraefikToAnotherResolverCountsEveryHostnameAgain(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	to := traefikOnTheHost()
	resolved := *to.Traefik
	resolved.Resolver = "le-dns"
	move := movePlanned(t, boxRecordedFor(t, tier, to), tier, Front{Traefik: &resolved})
	wantChange(t, move, "3 hostnames get new certificates from your Traefik", provider.ActionCreate)
}

func TestMovingABoxBetweenTwoOfYourProxiesPlansTheOutageFromTheOldStoppingUntilTheNewServes(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	move := movePlanned(t, boxRecordedFor(t, tier, caddyService()), tier, coolifysTraefik())
	if want := "outage from your Caddy stopping until Coolify's Traefik serves"; move.Reason != want {
		t.Errorf("the move gives its outage as %q, want %q", move.Reason, want)
	}
	wantChange(t, move, "/etc/caddy/ocel.d/ocel.caddy", provider.ActionDelete)
	wantChange(t, move, sudoersCaddyReload, provider.ActionDelete)
	wantChange(t, move, "/data/coolify/proxy/dynamic/ocel.yml", provider.ActionCreate)
	wantChange(t, move, "3 hostnames get new certificates from Coolify's Traefik", provider.ActionCreate)
}

func TestMovingABoxOntoOneHostnameCountsItAsOneCertificate(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	table := string(mustWrite(t, state))
	box := bootstrappedBehind(t, tier, Front{}, &table, nil)
	recordOn(t, box, tier, Front{}, "blog")
	wantChange(t, movePlanned(t, box, tier, routedByHand()), "1 hostname gets a new certificate from a proxy you route yourself", provider.ActionCreate)
}

func TestMovingABoxOffYourProxyIsRefusedWhileAnythingHoldsTheServingPortsNamingWhatToStop(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		from, to Front
		held     func(*bench)
		want     string
	}{
		"your Traefik to ocel's own proxy": {
			from: traefikOnTheHost(), to: Front{},
			held: func(box *bench) {
				portsOwnedOn(box, map[string]string{caddy.HTTPPort: "traefik\n", caddy.HTTPSPort: "traefik\n"})
			},
			want: "container traefik publishes :80 and :443, which ocel's own proxy takes over from your Traefik\n" +
				"Ocel never stops a proxy it does not run: run `docker stop traefik`, then run `ocel bootstrap production` again",
		},
		"a proxy you route yourself to your Traefik": {
			from: routedByHand(), to: traefikOnTheHost(),
			held: func(box *bench) { portsOwnedOn(box, nil, socketOwner{80, "nginx"}, socketOwner{443, "nginx"}) },
			want: "nginx listens on :80 and :443, which your Traefik takes over from a proxy you route yourself\n" +
				"Ocel never stops a proxy it does not run: stop nginx, then run `ocel bootstrap production` again",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			box := boxRecordedFor(t, environment.TierProduction, tc.from)
			tc.held(box)
			refusedBeforeWriting(t, box, tc.to, tc.want)
		})
	}
}

func probedHostname(command string) (string, bool) {
	_, probed, found := strings.Cut(command, quoted("probe")+" ")
	return strings.Trim(probed, "'"), found
}

func servedFrom(box *bench, answer func(hostname string) session.Result) {
	prior := box.answer
	box.answer = func(command string) (session.Result, bool) {
		if hostname, probed := probedHostname(command); probed {
			return answer(hostname), true
		}
		return prior(command)
	}
}

func throughTheSwitchboard(string) session.Result {
	return session.Result{Stdout: string(switchboard.RouterKind) + "\n"}
}

func movedTo(t *testing.T, box *bench, tier environment.Tier, to Front) ([]string, error) {
	t.Helper()
	var said []string
	err := NewBootstrap(box.fronted(to), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "the-suite"}, saying(&said))
	return said, err
}

func wantBefore(t *testing.T, box *bench, first, then string) {
	t.Helper()
	at, later := box.at(first), box.at(then)
	switch {
	case at < 0:
		t.Errorf("the move never ran %s:\n%s", first, strings.Join(box.commands(), "\n"))
	case later < 0:
		t.Errorf("the move never ran %s:\n%s", then, strings.Join(box.commands(), "\n"))
	case at > later:
		t.Errorf("the move ran %s at %d, after %s at %d, want it before", first, at, then, later)
	}
}

const switchboardRun = "'--name' 'ocel-switchboard'"

func TestMovingABoxFromOcelsOwnProxyToYoursTakesOcelsProxyAwayStandsYoursAndWaitsUntilItServesEveryHostname(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := boxRecordedFor(t, tier, Front{})
	servedFrom(box, throughTheSwitchboard)
	said, err := movedTo(t, box, tier, traefikOnTheHost())
	if err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	wantBefore(t, box, "docker rm --force "+quoted(caddy.Container), switchboardRun)
	wantBefore(t, box, "rm -rf "+quoted(ProxyData), switchboardRun)
	wantBefore(t, box, switchboardRun, quoted("place")+" "+quoted("/etc/traefik/dynamic/ocel.yml"))
	wantBefore(t, box, quoted("place")+" "+quoted("/etc/traefik/dynamic/ocel.yml"), "/dev/stdin "+quoted(FrontRecordPath))
	if record := box.fed[box.at("/dev/stdin "+quoted(FrontRecordPath))]; !strings.Contains(record, `"project":"shop"`) || !strings.Contains(record, `"traefik"`) {
		t.Errorf("%s was written as %s, want your Traefik recorded as set by shop, the project that moved the box", FrontRecordPath, record)
	}
	if at := box.at(quoted("--any-certificate")); at >= 0 {
		t.Errorf("the move waited on your Traefik before telling you to start it: %s", box.commands()[at])
	}
	if !slices.Contains(said, "Start your proxy on 80 and 443 now") {
		t.Errorf("the move said %q, want it to tell you to start your proxy", said)
	}
	for _, hostname := range []string{"box.example.com", claimed, "www." + claimed} {
		wantBefore(t, box, "/dev/stdin "+quoted(FrontRecordPath), quoted("probe")+" "+quoted(hostname))
	}
}

func TestMovingABoxFromYourProxyToOcelsOwnTakesOcelsFileOutUnreloadedBeforeTheSwitchboardLeavesAndStandsOcelsProxy(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := boxRecordedFor(t, tier, coolifysTraefik())
	servedFrom(box, func(string) session.Result {
		return session.Result{Code: proxyNotServingYet, Stderr: "nothing is started yet"}
	})
	said, err := movedTo(t, box, tier, Front{})
	if err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	wantBefore(t, box, quoted("unplace")+" "+quoted("/data/coolify/proxy/dynamic/ocel.yml"), switchboardRun)
	wantBefore(t, box, switchboardRun, "'--name' "+quoted(caddy.Container))
	board := box.commands()[box.at(switchboardRun)]
	if strings.Contains(board, quoted("coolify")) {
		t.Errorf("the switchboard was started again as %s, want it off coolify, the network of the proxy the box left", board)
	}
	for _, command := range box.commands() {
		if _, probed := probedHostname(command); probed || strings.Contains(command, quoted("--any-certificate")) {
			t.Errorf("the move asked your stopped Traefik to serve: %s", command)
		}
	}
	if slices.Contains(said, "Start your proxy on 80 and 443 now") {
		t.Errorf("the move said %q, want nothing asked of you: ocel's own proxy orders its certificates on the first handshake", said)
	}
	if record := box.fed[box.at("/dev/stdin "+quoted(FrontRecordPath))]; !strings.Contains(record, `"proxy":null`) || !strings.Contains(record, `"project":"shop"`) {
		t.Errorf("%s was written as %s, want ocel's own proxy recorded as set by shop", FrontRecordPath, record)
	}
}

func TestMovingABoxWithinYourOwnProxyPlacesTheNewFileWaitsUntilItServesAndOnlyThenTakesTheOldOut(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := boxRecordedFor(t, tier, traefikOnTheHost())
	servedFrom(box, throughTheSwitchboard)
	said, err := movedTo(t, box, tier, traefikIn("/etc/traefik/dynamic/ocel"))
	if err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	wantBefore(t, box, switchboardRun, quoted("place")+" "+quoted("/etc/traefik/dynamic/ocel/ocel.yml"))
	wantBefore(t, box, quoted("place")+" "+quoted("/etc/traefik/dynamic/ocel/ocel.yml"), quoted("probe")+" "+quoted(claimed))
	wantBefore(t, box, quoted("probe")+" "+quoted(claimed), "rm -f "+quoted("/etc/traefik/dynamic/ocel.yml"))
	if at := box.at(quoted("unplace") + " " + quoted("/etc/traefik/dynamic/ocel.yml")); at >= 0 {
		t.Errorf("the move asked the switchboard to take out a file in a directory it no longer mounts: %s", box.commands()[at])
	}
	if slices.Contains(said, "Start your proxy on 80 and 443 now") {
		t.Errorf("the move said %q, want nothing asked of a Traefik that serves throughout", said)
	}
}

func TestMovingABoxWithinYourOwnCaddyReloadsItOnceTheOldFileIsOut(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := boxRecordedFor(t, tier, caddyService())
	servedFrom(box, throughTheSwitchboard)
	prior := box.answer
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "/config/apps/http/servers") {
			return session.Result{Stdout: "{}"}, true
		}
		return prior(command)
	}
	moving := caddyService()
	moving.Caddy.Directory = "/etc/caddy/sites"
	if _, err := movedTo(t, box, tier, moving); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	taken := box.at("rm -f " + quoted("/etc/caddy/ocel.d/ocel.caddy"))
	if taken < 0 {
		t.Fatalf("the move never took the old file out:\n%s", strings.Join(box.commands(), "\n"))
	}
	if command := box.commands()[taken]; !strings.Contains(command, "systemctl") || strings.Contains(command, quoted("unplace-origins")) {
		t.Errorf("the old file was taken out as\n%s\nwant your Caddy reloaded after, and its origin files taken by path: the switchboard now mounts the new directory", command)
	}
}

func TestMovingABoxBetweenTwoOfYourProxiesTakesTheOldFileOutBeforeStandingTheNew(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := boxRecordedFor(t, tier, caddyService())
	servedFrom(box, throughTheSwitchboard)
	said, err := movedTo(t, box, tier, coolifysTraefik())
	if err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	unplaced := quoted("unplace") + " " + quoted("/etc/caddy/ocel.d/ocel.caddy")
	wantBefore(t, box, unplaced, switchboardRun)
	if command := box.commands()[box.at(unplaced)]; strings.Contains(command, "systemctl") {
		t.Errorf("the old file was taken out as\n%s\nwant no reload of a Caddy you have stopped", command)
	}
	wantBefore(t, box, "rm -rf "+quoted(sudoersCaddyReload), switchboardRun)
	wantBefore(t, box, switchboardRun, quoted("place")+" "+quoted("/data/coolify/proxy/dynamic/ocel.yml"))
	if !slices.Contains(said, "Start your proxy on 80 and 443 now") {
		t.Errorf("the move said %q, want it to tell you to start your proxy", said)
	}
	wantBefore(t, box, "/dev/stdin "+quoted(FrontRecordPath), quoted("probe")+" "+quoted(claimed))
}

func TestMovingABoxOntoYourProxyThatNeverServesIsRefusedNamingWhatItDidNotServeAndLeavesTheApplyUnfinished(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := boxRecordedFor(t, tier, Front{})
	servedFrom(box, func(hostname string) session.Result {
		if hostname == claimed {
			return session.Result{Code: proxyNotServingYet, Stderr: "connection refused on 127.0.0.1:443"}
		}
		return throughTheSwitchboard(hostname)
	})
	_, err := movedTo(t, box, tier, traefikOnTheHost())
	refused := refusalOf(t, err, refusal.CodeNotReady)
	for _, wanted := range []string{"your Traefik did not serve " + claimed + " through ocel's switchboard", "connection refused on 127.0.0.1:443", "ocel bootstrap production"} {
		if !strings.Contains(refused.Message, wanted) {
			t.Errorf("the refusal says %q, want %q in it", refused.Message, wanted)
		}
	}
	if strings.Contains(refused.Message, "www."+claimed) {
		t.Errorf("the refusal says %q, naming www.%s, which your Traefik served", refused.Message, claimed)
	}
	var waited time.Duration
	for _, wait := range box.waits() {
		waited += wait
	}
	if waited < moveWait {
		t.Errorf("the move gave up after %s, want it to wait %s for you to start your proxy", waited, moveWait)
	}
	for at, command := range box.commands() {
		if strings.Contains(command, "/dev/stdin "+quoted(StampPath(tier))) && strings.Contains(box.fed[at], string(StateComplete)) {
			t.Errorf("the move stamped the tier complete though your proxy never served: %s", command)
		}
	}
}

func TestADeployOntoABoxAnotherProxyFrontsIsStillRefusedAndToldThatBootstrapMovesTheBox(t *testing.T) {
	t.Parallel()

	tier := environment.TierPreview
	box := boxRecordedFor(t, tier, coolifysTraefik())
	refused := refusalOf(t, box.fronted(Front{}).FrontAgrees(context.Background()), refusal.CodeInvalid)
	for _, wanted := range []string{"add `\"proxy\": { \"traefik\": { \"preset\": \"coolify\" } }`", "`ocel bootstrap preview` moves the box"} {
		if !strings.Contains(refused.Message, wanted) {
			t.Errorf("the refusal says %q, want %q in it", refused.Message, wanted)
		}
	}
	if strings.Contains(refused.Message, "not supported") {
		t.Errorf("the refusal says %q, want the move bootstrap makes named instead", refused.Message)
	}
}

func TestMovingABoxFromOcelsOwnProxyIsNotRefusedForThePortsOcelsOwnProxyHoldsOrForYourProxyNotRunningYet(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	for name, to := range map[string]Front{
		"your Traefik on the host": traefikOnTheHost(),
		"Coolify's Caddy":          coolifysCaddy(),
		"your Caddy service":       caddyService(),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			box := boxRecordedFor(t, tier, Front{})
			portsOwnedOn(box, map[string]string{caddy.HTTPPort: caddy.Container + "\n", caddy.HTTPSPort: caddy.Container + "\n"})
			box.broke = func(command string) error {
				if strings.Contains(command, "2019") || strings.Contains(command, "'curl'") {
					return errors.New("your proxy is not running, and ocel-proxy holds 443 until the move takes it away")
				}
				return nil
			}
			movePlanned(t, box, tier, to)
		})
	}
}
