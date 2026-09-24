package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const (
	retiring    = "shop-web-older00000"
	apiRetiring = "shop-api-older00000"
	apiStanding = "shop-api-newer00000"
)

var (
	retired    = retiring + ":" + providerkit.InjectedPortText
	flipTo     = physical + ":" + providerkit.InjectedPortText
	apiRetired = apiRetiring + ":" + providerkit.InjectedPortText
	apiFlipTo  = apiStanding + ":" + providerkit.InjectedPortText
)

type watched struct {
	said  []string
	told  []string
	lines []string
}

func (w *watched) Say(message string) {
	w.said = append(w.said, message)
	w.lines = append(w.lines, message)
}

func (w *watched) Detail(message string) {
	w.told = append(w.told, message)
	w.lines = append(w.lines, message)
}

func (w *watched) at(fragment string) int {
	return slices.IndexFunc(w.lines, func(line string) bool { return strings.Contains(line, fragment) })
}

func (w *watched) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

func aRelease() Release {
	return Release{
		Apps:          []AppRelease{{RouteKey: keyed("web"), Target: flipTo, HealthPath: "/healthz"}},
		DeployTimeout: 30 * time.Second,
		DrainTimeout:  30 * time.Second,
	}
}

func bothApps() Release {
	rel := aRelease()
	rel.Apps = append(rel.Apps, AppRelease{RouteKey: keyed("api"), Target: apiFlipTo, HealthPath: "/up"})
	return rel
}

func documentOf(t *testing.T, state ProxyState) string {
	t.Helper()
	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(rendered)
}

func configFor(t *testing.T, upstream string) string {
	t.Helper()
	return documentOf(t, ProxyState{
		Grace:  30 * time.Second,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: upstream, Health: "/healthz"}},
	})
}

func twoAppsServing(t *testing.T) string {
	t.Helper()
	return documentOf(t, ProxyState{
		Grace: 30 * time.Second,
		Routes: []AppRoute{
			{RouteKey: keyed("web"), Upstream: retired, Health: "/healthz"},
			{RouteKey: keyed("api"), Upstream: apiRetired, Health: "/up"},
		},
	})
}

func gates(command string) bool { return strings.Contains(command, quoted("gate")) }

func flips(command string) bool { return strings.Contains(command, quoted("flip")) }

func idles(command string) bool { return strings.Contains(command, quoted("idle")) }

func everyIdle(command string) session.Result {
	_, asked, _ := strings.Cut(command, quoted("idle"))
	var idle strings.Builder
	for _, target := range strings.Fields(asked) {
		fmt.Fprintln(&idle, strings.Trim(target, "'"))
	}
	return session.Result{Stdout: idle.String()}
}

type flipped struct {
	*bench
	held string
}

func (f *flipped) at(fragment string) int { return f.bench.at(fragment) }

func (f *flipped) after(from int, match func(string) bool) int {
	for at, command := range f.commands() {
		if at > from && match(command) {
			return at
		}
	}
	return -1
}

func (f *flipped) count(match func(string) bool) int {
	counted := 0
	for _, command := range f.commands() {
		if match(command) {
			counted++
		}
	}
	return counted
}

func (f *flipped) cutover() int { return f.after(-1, flips) }

func (f *flipped) state(t *testing.T) ProxyState {
	t.Helper()
	f.mu.Lock()
	held := f.held
	f.mu.Unlock()
	state, err := ReadProxyState([]byte(held))
	if err != nil {
		t.Fatalf("%s was left as %q, which a restarted proxy cannot read: %v", ProxyConfig, held, err)
	}
	return state
}

func upstreamsOf(state ProxyState) map[string]string {
	upstreams := map[string]string{}
	for _, route := range state.Routes {
		upstreams[route.App] = route.Upstream
	}
	return upstreams
}

func benchedOn(t *testing.T, held string, gate, cutover session.Result) *flipped {
	t.Helper()
	stood := &flipped{bench: machine(nil), held: held}
	proxied := servesProxy(stood.bench, &stood.held)
	var once sync.Once
	stood.answer = func(command string) (session.Result, bool) {
		if result, mine := proxied(command); mine {
			return result, true
		}
		switch {
		case gates(command):
			return gate, true
		case idles(command):
			return everyIdle(command), true
		case flips(command):
			answered := session.Result{}
			once.Do(func() { answered = cutover })
			return answered, true
		default:
			return session.Result{}, false
		}
	}
	return stood
}

func benched(t *testing.T, gate, cutover session.Result) *flipped {
	t.Helper()
	return benchedOn(t, configFor(t, retired), gate, cutover)
}

func released(t *testing.T, rel Release, gate, cutover session.Result, report providerkit.Reporter) (*flipped, error) {
	t.Helper()
	stood := benched(t, gate, cutover)
	return stood, stood.host().Release(context.Background(), rel, report)
}

func unserved(err error) bool {
	var left Unserved
	return errors.As(err, &left)
}

func TestAPromotionOfEveryAppIsOneGateOneWriteAndOneFlip(t *testing.T) {
	t.Parallel()

	stood := benchedOn(t, twoAppsServing(t), session.Result{}, session.Result{})
	var posted string
	proxied := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if flips(command) && posted == "" {
			posted = stood.held
		}
		return proxied(command)
	}
	if err := stood.host().Release(context.Background(), bothApps(), nil); err != nil {
		t.Fatalf("Release(web and api) = %v", err)
	}

	gate := stood.at(quoted("gate"))
	if gate < 0 || stood.count(gates) != 1 {
		t.Fatalf("a promotion of two apps gated as %v, want one gate naming both targets", stood.commands())
	}
	for _, target := range []string{flipTo + "/healthz", apiFlipTo + "/up"} {
		if !strings.Contains(stood.commands()[gate], quoted(target)) {
			t.Errorf("the gate %q never probes %s", stood.commands()[gate], target)
		}
	}
	cutover := stood.cutover()
	written := 0
	for at, command := range stood.commands() {
		if writesProxy(command) && at < cutover {
			written++
			if at < gate {
				t.Errorf("the promotion wrote %s at %d, before the gate at %d: nothing is written before every target has passed", ProxyConfig, at, gate)
			}
		}
	}
	if written != 1 {
		t.Errorf("the promotion wrote %s %d times before its flip, want once: %v", ProxyConfig, written, stood.commands())
	}
	flip, err := ReadProxyState([]byte(posted))
	if err != nil {
		t.Fatal(err)
	}
	if got := upstreamsOf(flip); got["web"] != flipTo || got["api"] != apiFlipTo {
		t.Errorf("the one flip posted %v, want both apps onto their new targets at once: a box half on each promotion is the state this flip exists to rule out", got)
	}
	for _, retiree := range []string{retired, apiRetired} {
		if !strings.Contains(stood.commands()[cutover], quoted("--retire")+" "+quoted(retiree)) {
			t.Errorf("the flip %q never drains %s", stood.commands()[cutover], retiree)
		}
	}
	if flipped := stood.count(flips); flipped != 1 {
		t.Errorf("the promotion posted %d configurations, want the flip alone: caddy restarts every server on any change to what it runs, and a server shut down under load drops the connections it has accepted but not yet read: %v", flipped, stood.commands())
	}
	for _, stopped := range []string{retiring, apiRetiring} {
		if at := stood.at("docker stop " + quoted(stopped)); at < cutover {
			t.Errorf("%s was stopped at %d, before the flip at %d drained it", stopped, at, cutover)
		}
	}
	if got := upstreamsOf(stood.state(t)); got["web"] != flipTo || got["api"] != apiFlipTo {
		t.Errorf("the box's own file serves %v after the promotion", got)
	}
}

func TestAGateOneAppFailsWritesNothingAndFlipsNoApp(t *testing.T) {
	t.Parallel()

	before := twoAppsServing(t)
	stood := benchedOn(t, before,
		session.Result{Code: 4, Stdout: caddyadmin.Ungated + " " + apiFlipTo + "/up\n", Stderr: apiFlipTo + " never answered /up within 30s"},
		session.Result{})
	err := stood.host().Release(context.Background(), bothApps(), nil)
	if err == nil {
		t.Fatal("a promotion whose api never came up released successfully")
	}
	if !unserved(err) {
		t.Errorf("a failed gate refused with %v, which does not say the previous release still serves, and the ledger then keeps a pointer at a promotion the box never served", err)
	}
	if stood.after(-1, writesProxy) >= 0 || stood.cutover() >= 0 {
		t.Errorf("a failed gate still wrote or flipped the proxy: %v", stood.commands())
	}
	if stood.held != before {
		t.Errorf("a failed gate left %s changed", ProxyConfig)
	}
	for _, standing := range []string{physical, apiStanding} {
		if stood.at("docker rm --force "+quoted(standing)) < 0 {
			t.Errorf("a failed gate left %s standing with nothing routing to it: %v", standing, stood.commands())
		}
	}
	if !strings.Contains(err.Error(), "gate: http://"+apiFlipTo+"/up") {
		t.Errorf("the refusal reads\n%s\nand never names the gate that failed", err)
	}
	if strings.Contains(err.Error(), "gate: http://"+flipTo) {
		t.Errorf("the refusal reads\n%s\nand blames web, whose gate passed", err)
	}
	if stood.at(logCommand(apiStanding)) < 0 {
		t.Errorf("the refusal read no logs off %s, the container whose gate failed: %v", apiStanding, stood.commands())
	}
}

func TestAFlipThatFailsPutsEveryAppBackOntoItsPreviousUpstreamAndPostsIt(t *testing.T) {
	t.Parallel()

	stood := benchedOn(t, twoAppsServing(t), session.Result{},
		session.Result{Code: 2, Stderr: "the proxy answered /load with 400 Bad Request: unknown module"})
	err := stood.host().Release(context.Background(), bothApps(), nil)
	if err == nil {
		t.Fatal("a promotion whose flip the proxy rejected released successfully")
	}
	if !unserved(err) {
		t.Errorf("a flip put back refused with %v, which does not say the previous release still serves", err)
	}
	state := stood.state(t)
	if got := upstreamsOf(state); got["web"] != retired || got["api"] != apiRetired {
		t.Errorf("after a rejected flip %s serves %v, want both apps back on what served before", ProxyConfig, got)
	}
	if posted := stood.after(stood.cutover(), flips); posted < 0 {
		t.Errorf("the previous configuration was written back and never posted: %v", stood.commands())
	}
	for _, standing := range []string{physical, apiStanding} {
		if stood.at("docker rm --force "+quoted(standing)) < 0 {
			t.Errorf("a rejected flip left %s standing: %v", standing, stood.commands())
		}
	}
	for _, live := range []string{retiring, apiRetiring} {
		if stood.at("docker stop "+quoted(live)) >= 0 {
			t.Errorf("a rejected flip stopped %s, which is still what the box serves", live)
		}
	}
}

func TestADigestThatMovesAfterTheGateIsRecomposedAndWrittenWithoutGatingAgain(t *testing.T) {
	t.Parallel()

	neighbour := documentOf(t, ProxyState{
		Grace: 30 * time.Second,
		Routes: []AppRoute{
			{RouteKey: keyed("web"), Upstream: retired, Health: "/healthz"},
			{RouteKey: keyed("api"), Upstream: apiRetired, Health: "/up"},
			{RouteKey: RouteKey{Owner: otherSurface, Pointer: pointed, App: "web"}, Upstream: "blog-web-1:8080"},
		},
	})
	stood := benchedOn(t, twoAppsServing(t), session.Result{}, session.Result{})
	proxied := stood.answer
	writes := 0
	stood.answer = func(command string) (session.Result, bool) {
		if writesProxy(command) {
			stood.mu.Lock()
			writes++
			collided := writes == 1
			if collided {
				stood.held = neighbour
			}
			stood.mu.Unlock()
			if collided {
				return session.Result{Code: proxyMoved, Stderr: digested(neighbour)}, true
			}
		}
		return proxied(command)
	}

	if err := stood.host().Release(context.Background(), bothApps(), nil); err != nil {
		t.Fatalf("Release() beside a neighbour that wrote after the gate = %v: a moved digest is recomposed onto, not refused", err)
	}
	if gated := stood.count(gates); gated != 1 {
		t.Errorf("the promotion gated %d times, want once: the targets it gated are still the ones it writes, and only the write moved", gated)
	}
	state := stood.state(t)
	if got := upstreamsOf(state); got["web"] != flipTo || got["api"] != apiFlipTo {
		t.Errorf("the box serves %v, want both apps on their new targets", got)
	}
	if !slices.ContainsFunc(state.Routes, func(route AppRoute) bool { return route.Owner == otherSurface }) {
		t.Errorf("the recomposed write dropped the neighbour's route: %v", state.Routes)
	}
}

func TestAWriteThatKeepsMovingIsRefusedBusyAndFlipsNothing(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{}, session.Result{})
	proxied := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if writesProxy(command) {
			return session.Result{Code: proxyMoved, Stderr: "a digest another writer left"}, true
		}
		return proxied(command)
	}
	err := stood.host().Release(context.Background(), aRelease(), nil)
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
		t.Fatalf("a write refused on every attempt failed with %v, want %s: another writer holds the box, and the deploy is told to run again", err, providerkit.CodeBusy)
	}
	if !unserved(err) {
		t.Errorf("a write that never landed refused with %v, which does not say the previous release still serves", err)
	}
	if written := stood.count(writesProxy); written != proxyRewrites {
		t.Errorf("the release wrote %d times, want %d: the retry is bounded", written, proxyRewrites)
	}
	if stood.cutover() >= 0 {
		t.Errorf("a release that never wrote its configuration still flipped: %v", stood.commands())
	}
	if stood.at("docker rm --force "+quoted(physical)) < 0 {
		t.Errorf("a refused release left %s standing: %v", physical, stood.commands())
	}
}

func TestATargetTheBoxAlreadyServesIsNeverRemovedByAFailedRelease(t *testing.T) {
	t.Parallel()

	for what, answer := range map[string][2]session.Result{
		"a gate that read a status":    {{Code: 3, Stderr: "answered /healthz with status 500"}, {}},
		"a gate nothing ever answered": {{Code: 4, Stderr: "never answered /healthz"}, {}},
		"a flip":                       {{}, {Code: 2, Stderr: "the proxy answered /load with 400"}},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			stood := benchedOn(t, configFor(t, flipTo), answer[0], answer[1])
			if err := stood.host().Release(context.Background(), aRelease(), nil); err == nil {
				t.Fatalf("a release failing at %s released successfully", what)
			}
			if stood.at("docker rm --force "+quoted(physical)) >= 0 {
				t.Errorf("a re-promotion of the build already serving failed at %s and removed %s, the container the box still routes to", what, physical)
			}
		})
	}
}

func TestTheOldContainerIsStoppedOnlyAfterTheFlipReturnsAndNothingReloadsTheProxyAfterIt(t *testing.T) {
	t.Parallel()

	stood, err := released(t, aRelease(), session.Result{}, session.Result{}, &watched{})
	if err != nil {
		t.Fatalf("Release() = %v", err)
	}
	call := stood.cutover()
	stop := stood.at("docker stop " + quoted(retiring))
	if call < 0 || stop < 0 {
		t.Fatalf("a successful release ran %v", stood.commands())
	}
	if stop < call {
		t.Error("the old container is stopped before the flip that drains it returns")
	}
	for at, command := range stood.commands() {
		if at > call && (flips(command) || writesProxy(command)) {
			t.Errorf("the release ran %q after the flip: caddy restarts every server on any change to what it runs, and a server shut down under load drops the connections it has accepted but not yet read", command)
		}
	}
	if state := stood.state(t); state.Routes[0].Upstream != flipTo {
		t.Errorf("the box's own file names %q as the live upstream, and a proxy restart reads that file rather than what was posted", state.Routes[0].Upstream)
	}
	if strings.Contains(stood.held, retired) {
		t.Errorf("the box's own file still declares the retired upstream:\n%s", stood.held)
	}
}

func TestEveryWayTheGateOrTheFlipCanFailReachesTheSameEndState(t *testing.T) {
	t.Parallel()

	for what, answer := range map[string][2]session.Result{
		"a gate that read a status":            {{Code: 3, Stderr: "answered /healthz with status 404"}, {}},
		"a gate nothing ever answered":         {{Code: 4, Stderr: "never answered /healthz"}, {}},
		"a config the proxy rejected":          {{}, {Code: 2, Stderr: "the proxy answered /load with 400 Bad Request: unknown module"}},
		"an upstreams read it could not parse": {{}, {Code: 5, Stderr: "cannot parse the proxy's upstreams"}},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			stood, err := released(t, aRelease(), answer[0], answer[1], nil)
			if err == nil {
				t.Fatalf("%s released successfully", what)
			}
			var refusal providerkit.Refusal
			if !errors.As(err, &refusal) {
				t.Errorf("%s failed with %T, want a refusal the cli renders", what, err)
			}
			if !unserved(err) {
				t.Errorf("%s refused with %v, which does not say the previous release still serves", what, err)
			}
			if stood.at("docker stop "+quoted(retiring)) >= 0 {
				t.Errorf("%s stopped the retired container, and the box then serves nothing at all", what)
			}
			if stood.at("docker rm --force "+quoted(physical)) < 0 {
				t.Errorf("%s left the new container standing beside the old one: %v", what, stood.commands())
			}
			if got := upstreamsOf(stood.state(t)); got["web"] != retired {
				t.Errorf("%s left %s serving %v, naming an upstream the proxy never accepted, and a restart would adopt it", what, ProxyConfig, got)
			}
		})
	}
}

func TestAFailureTheFlipCanOnlyReachAfterItPostedPutsThePreviousConfigBackOnTheProxy(t *testing.T) {
	t.Parallel()

	for what, answered := range map[string]session.Result{
		"an upstreams read it could not parse": {Code: 5, Stderr: "cannot parse the proxy's upstreams"},
		"a socket that stopped answering":      {Code: 2, Stderr: "the proxy answered nothing over /run/caddy-admin.sock"},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			stood, err := released(t, aRelease(), session.Result{}, answered, nil)
			if err == nil {
				t.Fatalf("%s released successfully", what)
			}
			called := stood.cutover()
			wrote, posted := stood.after(called, writesProxy), stood.after(called, flips)
			if posted < 0 {
				t.Fatalf("%s rolled the file back and never re-posted it, so the proxy is left live-routing to an upstream this loop then removes: %v", what, stood.commands())
			}
			if wrote < 0 || posted < wrote {
				t.Errorf("%s posted a configuration it had not written back first: %v", what, stood.commands())
			}
			if removed := stood.at("docker rm --force " + quoted(physical)); removed < 0 || removed < posted {
				t.Errorf("%s removed %s before the proxy was put back onto %s: %v", what, physical, retired, stood.commands())
			}
		})
	}
}

func TestAFlipThatNeverReturnedAnExitCodeEndsWhereANonZeroOneDoes(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{}, session.Result{})
	var once sync.Once
	stood.broke = func(command string) error {
		var err error
		if flips(command) {
			once.Do(func() { err = errors.New("ssh: connection reset by peer") })
		}
		return err
	}
	err := stood.host().Release(context.Background(), aRelease(), nil)
	if err == nil {
		t.Fatal("a flip that never came back released successfully")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) {
		t.Errorf("a flip that never came back failed with %T, want the refusal every other failure renders", err)
	}
	if !strings.Contains(err.Error(), "connection reset by peer") {
		t.Errorf("a flip that never came back is refused with\n%s\nand never names what went wrong", err)
	}
	if stood.at("docker stop "+quoted(retiring)) >= 0 {
		t.Errorf("a flip that never came back stopped the retired container: %v", stood.commands())
	}
	if stood.at("docker rm --force "+quoted(physical)) < 0 {
		t.Errorf("a flip that never came back left the new container standing: %v", stood.commands())
	}
	if got := upstreamsOf(stood.state(t)); got["web"] != retired {
		t.Errorf("a flip that never came back left %s serving %v, an upstream nothing here saw the proxy accept", ProxyConfig, got)
	}
	if stood.after(stood.cutover(), flips) < 0 {
		t.Errorf("a flip that never came back never re-posted the previous configuration, and it may have posted before the connection died: %v", stood.commands())
	}
}

func TestAFirstDeployThatFailsLeavesNothingServingAndIsNotAPathOfItsOwn(t *testing.T) {
	t.Parallel()

	stood := benchedOn(t, documentOf(t, ProxyState{Grace: 30 * time.Second}),
		session.Result{Code: 3, Stderr: "answered /healthz with status 500"}, session.Result{})
	if err := stood.host().Release(context.Background(), aRelease(), nil); err == nil {
		t.Fatal("a first deploy whose app never came up released successfully")
	}
	if stood.at("docker stop") >= 0 {
		t.Errorf("a first deploy stopped something, and there was nothing to stop: %v", stood.commands())
	}
	if stood.at("docker rm --force "+quoted(physical)) < 0 {
		t.Errorf("a first deploy left its container standing: %v", stood.commands())
	}
}

func TestAReleaseCarryingNoHealthPathIsRefusedBeforeTheHelperEverRuns(t *testing.T) {
	t.Parallel()

	for what, path := range map[string]string{"an empty path": "", "a path of blanks": "  "} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			blank := bothApps()
			blank.Apps[1].HealthPath = path
			stood, err := released(t, blank, session.Result{}, session.Result{}, nil)
			if err == nil {
				t.Fatalf("a release carrying %s released successfully", what)
			}
			var refusal providerkit.Refusal
			if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
				t.Errorf("a release carrying %s failed with %v, want the seam to name what is missing rather than the helper's usage error", what, err)
			}
			if !strings.Contains(err.Error(), healthKey) || !strings.Contains(err.Error(), "api") {
				t.Errorf("a release carrying %s is refused with\n%s\nwhich never names the app or the key that sets it", what, err)
			}
			if stood.at(quoted("gate")) >= 0 || stood.after(-1, writesProxy) >= 0 {
				t.Errorf("a release carrying %s reached the helper or rewrote %s: %v", what, ProxyConfig, stood.commands())
			}
		})
	}
}

func TestAReleaseWithNothingToRetireNeverAsksForADrain(t *testing.T) {
	t.Parallel()

	stood := benchedOn(t, documentOf(t, ProxyState{Grace: 30 * time.Second}), session.Result{}, session.Result{})
	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	for _, command := range stood.commands() {
		if strings.Contains(command, quoted("--retire")) {
			t.Errorf("a first deploy asked the helper to drain: %q", command)
		}
	}
	if stood.at("docker stop") >= 0 {
		t.Errorf("a first deploy stopped a container it never had: %v", stood.commands())
	}
}

func TestTheFlipConfigMovesOnlyTheRouteAndTheHelperIsToldToDrainTheRetiredUpstream(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{}, session.Result{})
	var posted string
	proxied := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if flips(command) && posted == "" {
			posted = stood.held
		}
		return proxied(command)
	}
	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	want, err := RenderProxyConfig(ProxyState{
		Grace:  30 * time.Second,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: flipTo, Health: "/healthz"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if posted != string(want) {
		t.Errorf("the config the helper was handed is\n%s\nwant the running one with only the route moved:\n%s\nanything else it declares changes what caddy runs, and caddy restarts every server to take it", posted, want)
	}
	gated := stood.commands()[stood.at(quoted("gate"))]
	for _, wanted := range []string{
		quoted(flipTo + "/healthz"),
		quoted("--deploy-timeout") + " " + quoted("30"),
		quoted(ProxyHelperMount),
		quoted(ProxyContainer),
	} {
		if !strings.Contains(gated, wanted) {
			t.Errorf("the gate is made as %q, which carries no %s", gated, wanted)
		}
	}
	cut := stood.commands()[stood.cutover()]
	for _, wanted := range []string{
		quoted("--retire") + " " + quoted(retired),
		quoted("--drain-timeout") + " " + quoted("30"),
		quoted(ProxyConfigMount),
	} {
		if !strings.Contains(cut, wanted) {
			t.Errorf("the flip is made as %q, which carries no %s", cut, wanted)
		}
	}
}

func TestADrainThatReadZeroIsToldBeforeTheContainerItFreedIsStopped(t *testing.T) {
	t.Parallel()

	report := &watched{}
	_, err := released(t, aRelease(), session.Result{}, session.Result{Stdout: caddyadmin.Drained + " " + retired + "\n"}, report)
	if err != nil {
		t.Fatalf("Release() over a drain that read zero = %v", err)
	}
	drained := report.at(retiring + " reported nothing in flight")
	if drained < 0 {
		t.Fatalf("the release said %v and never that the drain read the retired upstream empty: the count reaches zero for about one drain poll before the config that carries the upstream is rewritten, so the drain's own word is the only thing that can witness it", report.lines)
	}
	if stopping := report.at("Stopping " + retiring); stopping < 0 || drained > stopping {
		t.Errorf("the release said %v, want the drain's outcome before %q: a report that names the stop first reads as though the container went while it was still serving", report.lines, "Stopping "+retiring)
	}
}

func TestADrainThatExpiresIsWarnedAboutRatherThanFailed(t *testing.T) {
	t.Parallel()

	report := &watched{}
	_, err := released(t, aRelease(), session.Result{}, session.Result{Stdout: caddyadmin.DrainExpired + " " + retired + " 2\n"}, report)
	if err != nil {
		t.Fatalf("Release() over an expired drain = %v, want the new release serving", err)
	}
	warned := strings.Join(report.told, "\n")
	if !strings.Contains(warned, retired) || !strings.Contains(warned, "2") {
		t.Errorf("an expired drain warned %q, want the count still in flight at expiry", warned)
	}
	if !strings.Contains(warned, "502") {
		t.Errorf("the warning reads %q and never states the ceiling's outcome", warned)
	}
	if !strings.Contains(strings.ToLower(warned), "websocket") {
		t.Errorf("the warning reads %q and leaves the fate of a hijacked connection implied", warned)
	}
}

func TestTheCompareAndSetOverTheProxyConfigIsOneCriticalSection(t *testing.T) {
	t.Parallel()

	written := stagedWrite("a-digest-this-deploy-read")
	locked := strings.Index(written, "flock -x")
	compared := strings.Index(written, `if [ "$held" != `)
	moved := strings.Index(written, `mv "$staged" `)
	if locked < 0 {
		t.Fatalf("the staged write is\n%s\nand takes no lock: the digest is read, compared and only then moved over, so two writers that read the same digest both pass the compare and the second mv drops the first one's routes — which is the update this compare-and-set exists to refuse", written)
	}
	if compared < locked || moved < locked {
		t.Errorf("the staged write is\n%s\nand compares or moves outside the lock it takes, which serializes nothing", written)
	}
	if !strings.Contains(written[:locked], "exec 9<"+quoted(ProxyConfig)) {
		t.Errorf("the staged write is\n%s\nand locks something other than %s, so a writer of that file contends with nothing", written, ProxyConfig)
	}
	if strings.Index(written, `cat > "$staged"`) > locked {
		t.Errorf("the staged write is\n%s\nand reads the whole document off the wire with the lock held, which stalls every other writer on this box for the length of an ssh transfer", written)
	}
}

func TestTwoWritersThatReadTheSameDigestLeaveOneOfTheirDocumentsBehind(t *testing.T) {
	t.Parallel()

	for _, needed := range []string{"sh", "flock", "sha256sum", "mktemp"} {
		if _, err := exec.LookPath(needed); err != nil {
			t.Skipf("no %s on this machine, and the write under test is the shell one a box runs", needed)
		}
	}
	config := filepath.Join(t.TempDir(), "caddy.json")
	if err := os.WriteFile(config, []byte("the config both writers read\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	read := contentSum([]byte("the config both writers read\n"))

	racing := make(chan error, 2)
	for _, document := range []string{"written by one\n", "written by the other\n"} {
		go func() {
			run := exec.Command("/bin/sh", "-c", strings.ReplaceAll(stagedWrite(read), ProxyConfig, config))
			run.Stdin = strings.NewReader(document)
			racing <- run.Run()
		}()
	}
	won := 0
	for range 2 {
		if err := <-racing; err == nil {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d of two writers that read the same digest were told they had written it, want one: the other composed its routes onto a file it no longer holds", won)
	}
	held, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if string(held) != "written by one\n" && string(held) != "written by the other\n" {
		t.Errorf("the file holds %q, want one writer's whole document", held)
	}
}

func TestAReleaseComposesItsRouteOntoWhatAConcurrentDeployLeftRatherThanRefusing(t *testing.T) {
	t.Parallel()

	neighbours := map[int]string{
		1: documentOf(t, ProxyState{
			Grace:  30 * time.Second,
			Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: retired}, {RouteKey: keyed("api"), Upstream: "prod-api-1:8080"}},
		}),
	}
	stood := benched(t, session.Result{}, session.Result{})
	proxied := stood.answer
	writes := 0
	stood.answer = func(command string) (session.Result, bool) {
		if writesProxy(command) {
			stood.mu.Lock()
			writes++
			neighbour, collided := neighbours[writes]
			if collided {
				stood.held = neighbour
			}
			stood.mu.Unlock()
			if collided {
				return session.Result{Code: proxyMoved, Stderr: digested(neighbour)}, true
			}
		}
		return proxied(command)
	}

	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() beside a deploy that rewrote %s after it was read = %v: the compare-and-set exists to refuse a lost update, not a neighbour", ProxyConfig, err)
	}
	state := stood.state(t)
	if got := upstreamsOf(state); got["web"] != flipTo || got["api"] != "prod-api-1:8080" {
		t.Errorf("the box serves %v after the release, want web onto %s beside the neighbour's api: the retry must compose onto what it re-read, not onto what it first read", got, flipTo)
	}
}

func TestAFailureAfterTheFlipSaysTheReleaseIsServingAndNamesWhatIsLeftBehind(t *testing.T) {
	t.Parallel()

	report := &watched{}
	stood := benched(t, session.Result{}, session.Result{Stdout: caddyadmin.DrainExpired + " " + retired + " 2\n"})
	proxied := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if command == "docker stop "+quoted(retiring)+" >/dev/null" {
			return session.Result{Code: 1, Stderr: "no space left on device"}, true
		}
		return proxied(command)
	}

	err := stood.host().Release(context.Background(), aRelease(), report)
	if err == nil {
		t.Fatal("the stop after the flip was refused and the release reported success")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a failure after the flip failed with %T (%v), want the refusal every other failure renders: a bare machine error reads as a failed release while the new one is in fact serving", err, err)
	}
	if unserved(err) {
		t.Errorf("a failure after the flip refused with %v as though the previous release still served, and the ledger would then point away from the release that is live", err)
	}
	said := err.Error()
	for what, wanted := range map[string]string{
		"that the flip already landed":    "flipped",
		"the release that is now serving": physical,
		"the container left running":      retiring,
		"why the stop failed":             "no space left on device",
	} {
		if !strings.Contains(said, wanted) {
			t.Errorf("a failure after the flip is refused with\n%s\nand that names no %s (%s)", said, what, wanted)
		}
	}
	warned := strings.Join(report.told, "\n")
	if !strings.Contains(warned, retired) || !strings.Contains(warned, "502") {
		t.Errorf("the release reported %q; the drain expired holding requests open and the stop that failed after the flip swallowed the warning", warned)
	}
}

func diagnosed(t *testing.T, gate session.Result, state, logs string) string {
	t.Helper()
	stood := benched(t, gate, session.Result{})
	proxied := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "docker inspect") && strings.Contains(command, ".State."):
			return session.Result{Stdout: state}, true
		case strings.Contains(command, "docker logs"):
			return session.Result{Stdout: logs}, true
		default:
			return proxied(command)
		}
	}
	err := stood.host().Release(context.Background(), aRelease(), nil)
	if err == nil {
		t.Fatal("a release the gate refused returned no error at all")
	}
	read, removed := stood.at("docker logs"), stood.at("docker rm --force "+quoted(physical))
	if read < 0 || stood.at("docker inspect --type container --format "+quoted(strings.Join(stateSelectors(), " "))) < 0 {
		t.Fatalf("a refused release captured no evidence at all: %v", stood.commands())
	}
	if removed < read {
		t.Error("the new container was removed before its logs were read, and a removed container answers neither logs nor inspect")
	}
	return err.Error()
}

func TestAHungAppIsDiagnosedByTheCombinationAndNeverByOneLine(t *testing.T) {
	t.Parallel()

	said := diagnosed(t,
		session.Result{Code: 4, Stderr: physical + ":" + providerkit.InjectedPortText + " never answered /healthz within 30s"},
		"Status=running ExitCode=0 OOMKilled=false Error= StartedAt=2026-01-01T00:00:00Z FinishedAt=0001-01-01T00:00:00Z RestartCount=0", "")

	for what, wanted := range map[string]string{
		"the verdict the helper reached":      "never answered",
		"the exact target it probed":          physical + ":" + providerkit.InjectedPortText,
		"the path it probed":                  "/healthz",
		"the config key that changes it":      healthKey,
		"the deploy timeout that expired":     "30s",
		"the state that says it never exited": "Status=running",
		"the restart count":                   "RestartCount=0",
		"the absence of logs, said out loud":  noLogOutput,
	} {
		if !strings.Contains(said, wanted) {
			t.Errorf("a hung app is refused with\n%s\nand that names no %s (%s)", said, what, wanted)
		}
	}
}

func TestTheEvidenceIsNotCutToFourLinesTheWayEveryOtherRefusalOnThisHostIs(t *testing.T) {
	t.Parallel()

	var written strings.Builder
	for at := range 10 {
		fmt.Fprintf(&written, "helper line %d\n", at)
	}
	said := diagnosed(t, session.Result{Code: 3, Stderr: written.String()},
		"Status=exited ExitCode=1 OOMKilled=false Error= StartedAt=2026-01-01T00:00:00Z FinishedAt=2026-01-01T00:00:01Z RestartCount=0",
		"2026-01-01T00:00:01Z panic: no such table\n")

	for at := range 10 {
		if !strings.Contains(said, fmt.Sprintf("helper line %d", at)) {
			t.Fatalf("the refusal reads\n%s\nand line %d of the helper's verdict is gone: the four-line formatter every other refusal on this host uses would take the evidence with it", said, at)
		}
	}
	if !strings.Contains(said, "panic: no such table") {
		t.Errorf("the refusal reads\n%s\nand carries none of what the container wrote", said)
	}
}

func TestExitedRestartingAndHungReadAsThreeDifferentThings(t *testing.T) {
	t.Parallel()

	shapes := map[string]string{
		"exited":     "Status=exited ExitCode=1 OOMKilled=false Error= StartedAt=2026-01-01T00:00:00Z FinishedAt=2026-01-01T00:00:01Z RestartCount=0",
		"restarting": "Status=restarting ExitCode=1 OOMKilled=false Error= StartedAt=2026-01-01T00:00:09Z FinishedAt=2026-01-01T00:00:09Z RestartCount=7",
		"hung":       "Status=running ExitCode=0 OOMKilled=false Error= StartedAt=2026-01-01T00:00:00Z FinishedAt=0001-01-01T00:00:00Z RestartCount=0",
	}
	read := map[string]string{}
	for what, state := range shapes {
		read[what] = diagnosed(t, session.Result{Code: 4, Stderr: "never answered /healthz"}, state, "")
	}
	for what, said := range read {
		for other, beside := range read {
			if what != other && said == beside {
				t.Errorf("%s and %s are refused with the same words, and the restart policy makes a crash loop invisible without them", what, other)
			}
		}
		if what == "hung" {
			continue
		}
		if !strings.Contains(said, "Status="+what) {
			t.Errorf("%s is refused with\n%s\nand never names the status it was found in", what, said)
		}
	}
}

func refusedAfter(t *testing.T, putBack session.Result) (*flipped, error) {
	t.Helper()
	stood := benched(t, session.Result{}, session.Result{Code: 5, Stderr: "cannot parse the proxy's upstreams"})
	proxied := stood.answer
	posted := 0
	stood.answer = func(command string) (session.Result, bool) {
		if flips(command) {
			stood.mu.Lock()
			posted++
			again := posted > 1
			stood.mu.Unlock()
			if again {
				return putBack, true
			}
		}
		return proxied(command)
	}
	err := stood.host().Release(context.Background(), aRelease(), nil)
	if err == nil {
		t.Fatal("a release the helper refused returned no error at all")
	}
	return stood, err
}

func TestARefusalNamesTheLiveUpstreamOnceAndTheAnswerFollowsWhetherTheProxyWasPutBack(t *testing.T) {
	t.Parallel()

	_, rolled := refusedAfter(t, session.Result{})
	if !strings.Contains(rolled.Error(), "the previous release is still live") {
		t.Errorf("a refusal that put the proxy back reads\n%s\nand never says which release is live", rolled)
	}
	stood, stranded := refusedAfter(t, session.Result{Code: 1, Stderr: "the proxy answered nothing over /run/caddy-admin.sock"})
	if strings.Contains(stranded.Error(), "the previous release is still live") {
		t.Errorf("a refusal that could not put the proxy back reads\n%s\nwhich asserts the previous release is live", stranded)
	}
	if !strings.Contains(stranded.Error(), physical) {
		t.Errorf("a refusal that could not put the proxy back reads\n%s\nand never names the container it left standing", stranded)
	}
	if unserved(stranded) {
		t.Errorf("a release whose proxy could not be put back refused with %v as though the previous release still served: the live release is unknown", stranded)
	}
	if stood.at("docker rm --force "+quoted(physical)) >= 0 {
		t.Errorf("a release whose proxy could not be put back removed %s, which the proxy may be routing to", physical)
	}
}

func strandedByWrite(t *testing.T, landed bool, back session.Result) (*flipped, error) {
	t.Helper()
	stood := benched(t, session.Result{}, session.Result{})
	proxied := stood.answer
	writes := 0
	stood.answer = func(command string) (session.Result, bool) {
		if !writesProxy(command) {
			return proxied(command)
		}
		stood.mu.Lock()
		writes++
		first := writes == 1
		stood.mu.Unlock()
		if first {
			if landed {
				proxied(command)
			}
			return session.Result{Code: 1, Stderr: "no space left on device"}, true
		}
		if back.Code != 0 {
			return back, true
		}
		return proxied(command)
	}
	err := stood.host().Release(context.Background(), aRelease(), nil)
	if err == nil {
		t.Fatal("a release whose flip configuration was never written released successfully")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) {
		t.Errorf("a flip configuration that could not be written failed with %T, want the refusal every other failure renders", err)
	}
	return stood, err
}

func TestAFlipConfigurationThatCannotBeWrittenLeavesNeitherAStandingContainerNorAHalfWrittenFile(t *testing.T) {
	t.Parallel()

	for what, landed := range map[string]bool{"a write that never landed": false, "a write that landed and reported failure": true} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			stood, err := strandedByWrite(t, landed, session.Result{})
			if !unserved(err) {
				t.Errorf("%s refused with %v, which does not say the previous release still serves", what, err)
			}
			if stood.at(`"--retire"`) >= 0 || stood.at(quoted("--drain-timeout")) >= 0 {
				t.Errorf("%s still flipped onto the configuration: %v", what, stood.commands())
			}
			if stood.at("docker rm --force "+quoted(physical)) < 0 {
				t.Errorf("%s left %s standing with nothing routing to it: %v", what, physical, stood.commands())
			}
			if got := upstreamsOf(stood.state(t)); got["web"] != retired {
				t.Errorf("%s left %s naming %v rather than the upstream that is still live", what, ProxyConfig, got)
			}
			for detail, wanted := range map[string]string{
				"the file it could not write":  ProxyConfig,
				"why the write failed":         "no space left on device",
				"what became of the container": physical,
			} {
				if !strings.Contains(err.Error(), wanted) {
					t.Errorf("%s is refused with\n%s\nand that names no %s (%s)", what, err, detail, wanted)
				}
			}
		})
	}
}

func TestAFlipConfigurationThatCannotBeWrittenBackEitherNamesTheFileARestartWouldServe(t *testing.T) {
	t.Parallel()

	stood, err := strandedByWrite(t, true, session.Result{Code: 1, Stderr: "no space left on device"})
	if !strings.Contains(err.Error(), "not restored") || !strings.Contains(err.Error(), ProxyConfig) {
		t.Errorf("a %s that could be neither written nor put back is refused with\n%s\nwhich never says which file a restarted proxy would read", ProxyConfig, err)
	}
	if unserved(err) {
		t.Errorf("a %s left naming the new release refused with %v as though the previous release still served", ProxyConfig, err)
	}
	if stood.at("docker rm --force "+quoted(physical)) >= 0 {
		t.Errorf("the release removed %s while %s still names it", physical, ProxyConfig)
	}
}

func TestTheDrainContractIsStatedOnEveryReleaseThatRetiresSomething(t *testing.T) {
	t.Parallel()

	report := &watched{}
	if _, err := released(t, aRelease(), session.Result{}, session.Result{}, report); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	stated := strings.Join(report.told, "\n")
	for what, wanted := range map[string]string{
		"the window in-flight requests are given": "30s",
		"what a client past it receives":          "502",
		"the fate of a websocket":                 "websocket",
		"the fate of a stream":                    "server-sent-events",
	} {
		if !strings.Contains(stated, wanted) {
			t.Errorf("a release states\n%s\nwhich leaves %s implied", stated, what)
		}
	}

	quiet := &watched{}
	first := benchedOn(t, documentOf(t, ProxyState{Grace: 30 * time.Second}), session.Result{}, session.Result{})
	if err := first.host().Release(context.Background(), aRelease(), quiet); err != nil {
		t.Fatal(err)
	}
	if len(quiet.told) != 0 {
		t.Errorf("a first deploy states a drain contract for a container it does not have: %v", quiet.told)
	}
}

func interrupted(t *testing.T, at func(command string) bool, answer session.Result) (*flipped, context.Context) {
	t.Helper()
	stood := benched(t, session.Result{}, session.Result{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	proxied := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if at(command) {
			if writesProxy(command) {
				proxied(command)
			}
			cancel()
			return answer, true
		}
		return proxied(command)
	}
	return stood, ctx
}

func TestAReleaseInterruptedAtTheFlipStillPutsTheProxyBackAndRemovesWhatItStoodUp(t *testing.T) {
	t.Parallel()

	var once sync.Once
	stood, ctx := interrupted(t, func(command string) bool {
		hit := false
		if flips(command) {
			once.Do(func() { hit = true })
		}
		return hit
	}, session.Result{Code: 2, Stderr: "the proxy answered nothing"})
	err := stood.host().Release(ctx, aRelease(), nil)
	if err == nil {
		t.Fatal("a release whose flip failed under a cancelled context released successfully")
	}
	state := stood.state(t)
	if got := upstreamsOf(state); got["web"] != retired {
		t.Errorf("%s was left as %+v after the interrupted release, want the previous release put back: the context that carried the interrupt is the one the unwind ran under, so the unwind never ran", ProxyConfig, state)
	}
	if stood.at("docker rm --force "+quoted(physical)) < 0 {
		t.Errorf("the interrupted release left %s standing with nothing routing to it: %v", physical, stood.commands())
	}
	if !strings.Contains(err.Error(), "the previous release is still live") {
		t.Errorf("the refusal reads %q and does not say the previous release is still live", err)
	}
}

func TestAReleaseInterruptedAtItsFirstWriteStillPutsTheFileBackAndRemovesWhatItStoodUp(t *testing.T) {
	t.Parallel()

	writes := 0
	stood, ctx := interrupted(t, func(command string) bool {
		if !writesProxy(command) {
			return false
		}
		writes++
		return writes == 1
	}, session.Result{Code: 1, Stderr: "connection reset after the move"})
	err := stood.host().Release(ctx, aRelease(), nil)
	if err == nil {
		t.Fatal("a release whose flip configuration was never confirmed released successfully")
	}
	if stood.at("docker rm --force "+quoted(physical)) < 0 {
		t.Errorf("the interrupted release left %s standing with nothing routing to it: %v", physical, stood.commands())
	}
	if got := upstreamsOf(stood.state(t)); got["web"] != retired {
		t.Errorf("the interrupted release left %s serving %v: the write landed before the interrupt and was never put back", ProxyConfig, got)
	}
	if !strings.Contains(err.Error(), ProxyConfig+" restored") {
		t.Errorf("the refusal reads %q, and the stranded write was never put back under the cancelled context", err)
	}
}

func TestWhatFollowsTheFlipLeavesARouteAnotherReleaseFlippedSinceOnItsUpstream(t *testing.T) {
	t.Parallel()

	overtaking := "shop-web-overtaker:" + providerkit.InjectedPortText
	stood := benched(t, session.Result{}, session.Result{})
	proxied := stood.answer
	var once sync.Once
	stood.answer = func(command string) (session.Result, bool) {
		if flips(command) {
			once.Do(func() {
				stood.mu.Lock()
				stood.held = documentOf(t, ProxyState{
					Grace:  30 * time.Second,
					Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: overtaking, Health: "/healthz"}},
				})
				stood.mu.Unlock()
			})
		}
		return proxied(command)
	}

	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if got := upstreamsOf(stood.state(t))["web"]; got != overtaking {
		t.Errorf("the release left web routed to %s, want %s: a release that flipped the route since has drained and stopped %s, and routing back onto it serves 502s", got, overtaking, flipTo)
	}
}

func TestARetireeThatWouldNotStopSaysTheFlipTookAndStillStopsEveryOther(t *testing.T) {
	t.Parallel()

	stood := benchedOn(t, twoAppsServing(t), session.Result{}, session.Result{})
	proxied := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if command == "docker stop "+quoted(apiRetiring)+" >/dev/null" {
			return session.Result{Code: 1, Stderr: "no space left on device"}, true
		}
		return proxied(command)
	}

	err := stood.host().Release(context.Background(), bothApps(), nil)
	if err == nil {
		t.Fatal("a release whose retiree would not stop reported success")
	}
	if unserved(err) {
		t.Errorf("a release whose retiree would not stop after the flip refused with %v as though the previous release still served, and the ledger would then point away from the release that is live", err)
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) {
		t.Errorf("a release whose retiree would not stop after the flip failed with %T, want the refusal every other failure renders", err)
	}
	for detail, wanted := range map[string]string{
		"that the flip took":       "flipped",
		"what failed":              "no space left on device",
		"the release that is live": physical,
	} {
		if !strings.Contains(err.Error(), wanted) {
			t.Errorf("a release whose retiree would not stop is refused with\n%s\nand that names no %s (%s)", err, detail, wanted)
		}
	}
	if stood.at("docker stop "+quoted(retiring)) < 0 {
		t.Errorf("a release whose other retiree would not stop never stopped %s, which it had drained: %v", retiring, stood.commands())
	}
}

func TestARetireeAlreadyGoneFromTheBoxIsNoStopTheReleaseFailedToMake(t *testing.T) {
	t.Parallel()

	for what, running := range map[string]string{
		"says it is gone":          retiring + " gone\n",
		"says it is not running":   retiring + " false\n",
		"says it is still running": retiring + " true\n",
		"never says":               "",
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			stood := benched(t, session.Result{}, session.Result{})
			proxied := stood.answer
			stood.answer = func(command string) (session.Result, bool) {
				switch {
				case command == "docker stop "+quoted(retiring)+" >/dev/null":
					return session.Result{Code: 1, Stderr: "Error response from daemon: No such container: " + retiring}, true
				case strings.Contains(command, ".State.Running"):
					return session.Result{Stdout: running}, true
				}
				return proxied(command)
			}

			err := stood.host().Release(context.Background(), aRelease(), nil)
			settled := !strings.HasSuffix(running, " true\n") && running != ""
			if settled && err != nil {
				t.Errorf("a release whose refused stop of %s was followed by a box that %s = %v, want it served: a container that is not running is the end the stop was for",
					retiring, what, err)
			}
			if !settled && (err == nil || !strings.Contains(err.Error(), "still running")) {
				t.Errorf("a release whose refused stop of %s was followed by a box that %s = %v, want it to name %s as still running",
					retiring, what, err, retiring)
			}
		})
	}
}

func TestWhatFollowsTheFlipNeverStopsARetireeARouteDialsAgainOrAnotherFlipIsDraining(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{}, session.Result{})
	proxied := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if idles(command) {
			return session.Result{}, true
		}
		return proxied(command)
	}

	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if stood.at("docker stop "+quoted(retiring)) >= 0 {
		t.Errorf("the release stopped %s after the proxy said it is still held: a rollback that flipped a route back onto it while this release drained it then serves a stopped container", retiring)
	}
}

func TestWhatFollowsTheFlipNeverStopsARetireeARollbackRoutedBeforeItsFlipRenamedTheRouteFile(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{}, session.Result{})
	proxied := stood.answer
	var once sync.Once
	stood.answer = func(command string) (session.Result, bool) {
		if idles(command) {
			once.Do(func() {
				stood.mu.Lock()
				stood.held = configFor(t, retired)
				stood.mu.Unlock()
			})
		}
		return proxied(command)
	}

	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if stood.at("docker stop "+quoted(retiring)) >= 0 {
		t.Errorf("the release stopped %s while %s routes web to it: the rollback that wrote that route flips onto a stopped container and web answers 502 until the next deploy", retiring, ProxyConfig)
	}
}

func TestWhatFollowsTheFlipCannotAskWhetherItsRetireeIsIdleStopsNothingAndSaysSo(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{}, session.Result{})
	proxied := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if idles(command) {
			return session.Result{Code: 5, Stderr: "permission denied reading /proc"}, true
		}
		return proxied(command)
	}

	err := stood.host().Release(context.Background(), aRelease(), nil)
	if stood.at("docker stop "+quoted(retiring)) >= 0 {
		t.Errorf("the release stopped %s without knowing whether a route dials it again", retiring)
	}
	if err == nil || !strings.Contains(err.Error(), retiring) || !strings.Contains(err.Error(), "permission denied reading /proc") {
		t.Errorf("Release() = %v, want it to name %s as still running and why", err, retiring)
	}
}

func TestAReleaseWhosePromotionWasOvertakenWhileItGatedWritesNothing(t *testing.T) {
	t.Parallel()

	before := configFor(t, retired)
	stood := benchedOn(t, before, session.Result{}, session.Result{})
	rel := aRelease()
	rel.Holding = func(context.Context) error {
		return providerkit.Refuse(providerkit.CodeBusy, "promotion p2 no longer holds production: p3 took it")
	}

	err := stood.host().Release(context.Background(), rel, nil)
	if err == nil {
		t.Fatal("a release whose promotion was overtaken released successfully")
	}
	if !unserved(err) {
		t.Errorf("an overtaken release refused with %v, which does not say it never flipped, and the ledger then keeps it in line to serve", err)
	}
	if !strings.Contains(err.Error(), "p3 took it") {
		t.Errorf("an overtaken release is refused with\n%s\nand never says what overtook it", err)
	}
	if stood.after(-1, writesProxy) >= 0 || stood.cutover() >= 0 {
		t.Errorf("an overtaken release still wrote or flipped the proxy: %v", stood.commands())
	}
	if stood.held != before {
		t.Errorf("an overtaken release left %s changed", ProxyConfig)
	}
	if stood.at("docker rm --force "+quoted(physical)) < 0 {
		t.Errorf("an overtaken release left %s standing with nothing routing to it: %v", physical, stood.commands())
	}
}

func TestAFailedReleaseLeavesATargetAnotherReleaseIsStillDrainingForThatReleaseToStop(t *testing.T) {
	t.Parallel()

	for what, answer := range map[string][2]session.Result{
		"a gate that read a status": {{Code: 3, Stderr: "answered /healthz with status 500"}, {}},
		"a flip":                    {{}, {Code: 2, Stderr: "the proxy answered /load with 400"}},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			stood := benched(t, answer[0], answer[1])
			answered := stood.answer
			stood.answer = func(command string) (session.Result, bool) {
				if idles(command) {
					return session.Result{}, true
				}
				return answered(command)
			}
			if err := stood.host().Release(context.Background(), aRelease(), nil); err == nil {
				t.Fatalf("a release failing at %s released successfully", what)
			}
			if stood.at("docker rm --force "+quoted(physical)) >= 0 {
				t.Errorf("a release failing at %s removed %s while the proxy said a flip was still draining it: the requests that flip is waiting out are cut, and the release that retired it stops it once they finish", what, physical)
			}
		})
	}
}

func TestAFailedReleaseThatCannotAskWhetherItsTargetIsIdleRemovesNothingAndSaysSo(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{Code: 3, Stderr: "answered /healthz with status 500"}, session.Result{})
	answered := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		if idles(command) {
			return session.Result{Code: 5, Stderr: "permission denied reading /proc"}, true
		}
		return answered(command)
	}
	err := stood.host().Release(context.Background(), aRelease(), nil)
	if err == nil {
		t.Fatal("a release whose gate failed released successfully")
	}
	if stood.at("docker rm --force "+quoted(physical)) >= 0 {
		t.Errorf("the release removed %s without knowing whether a flip was still draining it", physical)
	}
	if !strings.Contains(err.Error(), physical+" left standing") || !strings.Contains(err.Error(), "permission denied reading /proc") {
		t.Errorf("the refusal reads\n%s\nand never names %s as left standing or why", err, physical)
	}
}

func TestAReleaseOfNoAppsTouchesNothing(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{}, session.Result{})
	if err := stood.host().Release(context.Background(), Release{DeployTimeout: 30 * time.Second, DrainTimeout: 30 * time.Second}, nil); err != nil {
		t.Fatalf("Release() of no apps = %v", err)
	}
	if ran := stood.commands(); len(ran) != 0 {
		t.Errorf("a release of no apps ran %v on the box: a promotion whose apps stand nowhere on this box has nothing here to put in front", ran)
	}
}

func TestAFlipConfigurationThatLandedAndReportedFailurePutsEveryAppBack(t *testing.T) {
	t.Parallel()

	stood := benchedOn(t, twoAppsServing(t), session.Result{}, session.Result{})
	proxied := stood.answer
	writes := 0
	stood.answer = func(command string) (session.Result, bool) {
		if !writesProxy(command) {
			return proxied(command)
		}
		stood.mu.Lock()
		writes++
		first := writes == 1
		stood.mu.Unlock()
		if first {
			proxied(command)
			return session.Result{Code: 1, Stderr: "connection reset after the move"}, true
		}
		return proxied(command)
	}

	err := stood.host().Release(context.Background(), bothApps(), nil)
	if err == nil {
		t.Fatal("a promotion whose flip configuration reported failure released successfully")
	}
	if !unserved(err) {
		t.Errorf("a flip configuration put back refused with %v, which does not say the previous release still serves", err)
	}
	if !strings.Contains(err.Error(), ProxyConfig+" restored") {
		t.Errorf("the refusal reads\n%s\nand never says %s was put back", err, ProxyConfig)
	}
	state := stood.state(t)
	if got := upstreamsOf(state); got["web"] != retired || got["api"] != apiRetired {
		t.Errorf("%s serves %v after the put-back, want both apps back on what served before", ProxyConfig, got)
	}
	if stood.at(quoted("--retire")) >= 0 {
		t.Errorf("a flip configuration that reported failure was still flipped and drained: %v", stood.commands())
	}
	for _, standing := range []string{physical, apiStanding} {
		if stood.at("docker rm --force "+quoted(standing)) < 0 {
			t.Errorf("the put-back left %s standing with nothing routing to it: %v", standing, stood.commands())
		}
	}
}
