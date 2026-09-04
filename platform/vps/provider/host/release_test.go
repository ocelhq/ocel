package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/caddyadmin"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const (
	retiring = "shop-web-older00000"
	retired  = retiring + ":" + AppPort
	flipTo   = physical + ":" + AppPort
)

type watched struct {
	said []string
	told []string
}

func (w *watched) Say(message string)    { w.said = append(w.said, message) }
func (w *watched) Detail(message string) { w.told = append(w.told, message) }

func (w *watched) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

func aRelease() Release {
	return Release{
		RouteKey:      keyed("web"),
		Target:        flipTo,
		Retire:        retired,
		HealthPath:    "/healthz",
		DeployTimeout: 30 * time.Second,
		DrainTimeout:  30 * time.Second,
	}
}

func configFor(t *testing.T, upstream string) string {
	t.Helper()
	rendered, err := RenderProxyConfig(ProxyState{
		Grace:  30 * time.Second,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: upstream}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(rendered)
}

type flipped struct {
	*bench
	held string
}

func released(t *testing.T, rel Release, helper session.Result, report providerkit.Reporter) (*flipped, error) {
	t.Helper()
	stood := benched(t, helper)
	return stood, stood.host().Release(context.Background(), rel, report)
}

func benched(t *testing.T, helper session.Result) *flipped {
	t.Helper()
	stood := &flipped{bench: machine(nil), held: configFor(t, retired)}
	proxied := servesProxy(stood.bench, &stood.held)
	stood.answer = func(command string) (session.Result, bool) {
		if result, mine := proxied(command); mine {
			return result, true
		}
		switch {
		case strings.Contains(command, quoted("deploy")):
			return helper, true
		default:
			return session.Result{}, false
		}
	}
	return stood
}

func (f *flipped) at(fragment string) int { return f.bench.at(fragment) }

func TestTheOldContainerIsStoppedOnlyAfterTheCallReturnsSuccessAndTheSteadyStateFollowsIt(t *testing.T) {
	t.Parallel()

	report := &watched{}
	stood, err := released(t, aRelease(), session.Result{}, report)
	if err != nil {
		t.Fatalf("Release() = %v", err)
	}
	call := stood.at(quoted("deploy"))
	stop := stood.at("docker stop " + quoted(retiring))
	if call < 0 || stop < 0 {
		t.Fatalf("a successful release ran %v", stood.commands())
	}
	if stop < call {
		t.Error("the old container is stopped before the one call that gates, flips and drains returns, so a failed gate would leave nothing serving")
	}
	reload := -1
	for at, command := range stood.commands() {
		if at > stop && strings.Contains(command, quoted("flip")) {
			reload = at
			break
		}
	}
	if reload < 0 {
		t.Error("the steady-state configuration is never reloaded, so the proxy keeps the stopped container declared on its drain server")
	}
	if state, err := ReadProxyState([]byte(stood.held)); err != nil {
		t.Fatal(err)
	} else if state.Routes[0].Upstream != flipTo {
		t.Errorf("the box's own file names %q as the live upstream, and a proxy restart reads that file rather than what was posted", state.Routes[0].Upstream)
	}
	if strings.Contains(stood.held, retired) {
		t.Errorf("the box's own file still declares the retired upstream:\n%s", stood.held)
	}
}

func TestEveryWayTheOneCallCanFailReachesTheSameEndState(t *testing.T) {
	t.Parallel()

	for what, answered := range map[string]session.Result{
		"a gate that read a status":     {Code: 3, Stderr: "answered /healthz with status 404"},
		"a gate nothing ever answered":  {Code: 4, Stderr: "never answered /healthz"},
		"a config the proxy rejected":   {Code: 2, Stderr: "the proxy answered /load with 400 Bad Request: unknown module"},
		"a retired upstream gone early": {Code: 5, Stderr: "carries no upstream " + retired},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			stood, err := released(t, aRelease(), answered, nil)
			if err == nil {
				t.Fatalf("%s released successfully", what)
			}
			var refusal providerkit.Refusal
			if !errors.As(err, &refusal) {
				t.Errorf("%s failed with %T, want a refusal the cli renders", what, err)
			}
			if stood.at("docker stop "+quoted(retiring)) >= 0 {
				t.Errorf("%s stopped the retired container, and the box then serves nothing at all", what)
			}
			if stood.at("docker rm --force "+quoted(physical)) < 0 {
				t.Errorf("%s left the new container standing beside the old one: %v", what, stood.commands())
			}
			if !strings.Contains(stood.held, retired) {
				t.Errorf("%s left %s naming an upstream the proxy never accepted, and a restart would adopt it:\n%s", what, ProxyConfig, stood.held)
			}
		})
	}
}

func TestAFailureThePostCanOnlyReachAfterItLandedPutsThePreviousConfigBackOnTheProxy(t *testing.T) {
	t.Parallel()

	for what, answered := range map[string]session.Result{
		"a retired upstream gone from the pool": {Code: 5, Stderr: "carries no upstream " + retired},
		"a socket that stopped answering":       {Code: 2, Stderr: "the proxy answered nothing over /run/caddy-admin.sock"},
	} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()

			stood, err := released(t, aRelease(), answered, nil)
			if err == nil {
				t.Fatalf("%s released successfully", what)
			}
			called := stood.at(quoted("deploy"))
			wrote, posted := -1, -1
			for at, command := range stood.commands() {
				if at <= called {
					continue
				}
				if wrote < 0 && writesProxy(command) {
					wrote = at
				}
				if posted < 0 && strings.Contains(command, quoted("flip")) {
					posted = at
				}
			}
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

func TestACallThatNeverReturnedAnExitCodeEndsWhereANonZeroOneDoes(t *testing.T) {
	t.Parallel()

	stood := benched(t, session.Result{})
	stood.broke = func(command string) error {
		if strings.Contains(command, quoted("deploy")) {
			return errors.New("ssh: connection reset by peer")
		}
		return nil
	}
	err := stood.host().Release(context.Background(), aRelease(), nil)
	if err == nil {
		t.Fatal("a call that never came back released successfully")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) {
		t.Errorf("a call that never came back failed with %T, want the refusal every other failure renders", err)
	}
	if !strings.Contains(err.Error(), "connection reset by peer") {
		t.Errorf("a call that never came back is refused with\n%s\nand never names what went wrong", err)
	}
	if stood.at("docker stop "+quoted(retiring)) >= 0 {
		t.Errorf("a call that never came back stopped the retired container: %v", stood.commands())
	}
	if stood.at("docker rm --force "+quoted(physical)) < 0 {
		t.Errorf("a call that never came back left the new container standing: %v", stood.commands())
	}
	if !strings.Contains(stood.held, retired) {
		t.Errorf("a call that never came back left %s naming an upstream nothing here saw the proxy accept:\n%s", ProxyConfig, stood.held)
	}
	called := stood.at(quoted("deploy"))
	posted := -1
	for at, command := range stood.commands() {
		if at > called && strings.Contains(command, quoted("flip")) {
			posted = at
			break
		}
	}
	if posted < 0 {
		t.Errorf("a call that never came back never re-posted the previous configuration, and it may have flipped before the connection died: %v", stood.commands())
	}
}

func TestAFirstDeployThatFailsLeavesNothingServingAndIsNotAPathOfItsOwn(t *testing.T) {
	t.Parallel()

	first := aRelease()
	first.Retire = ""
	stood, err := released(t, first, session.Result{Code: 3, Stderr: "answered /healthz with status 500"}, nil)
	if err == nil {
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

			blank := aRelease()
			blank.HealthPath = path
			stood, err := released(t, blank, session.Result{}, nil)
			if err == nil {
				t.Fatalf("a release carrying %s released successfully", what)
			}
			var refusal providerkit.Refusal
			if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
				t.Errorf("a release carrying %s failed with %v, want the seam to name what is missing rather than the helper's usage error", what, err)
			}
			if !strings.Contains(err.Error(), healthKey) {
				t.Errorf("a release carrying %s is refused with\n%s\nwhich never names the key that sets it", what, err)
			}
			if stood.at(quoted("deploy")) >= 0 {
				t.Errorf("a release carrying %s reached the helper: %v", what, stood.commands())
			}
			if stood.at(`cat > "$staged"`) >= 0 {
				t.Errorf("a release carrying %s rewrote %s before it was refused: %v", what, ProxyConfig, stood.commands())
			}
		})
	}
}

func TestAReleaseWithNothingToRetireNeverAsksForADrain(t *testing.T) {
	t.Parallel()

	first := aRelease()
	first.Retire = ""
	stood, err := released(t, first, session.Result{}, nil)
	if err != nil {
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

func TestTheFlipConfigDeclaresTheRetiredUpstreamAndTheHelperIsToldToDrainIt(t *testing.T) {
	t.Parallel()

	var posted string
	stood := &flipped{bench: machine(nil), held: configFor(t, retired)}
	proxied := servesProxy(stood.bench, &stood.held)
	stood.answer = func(command string) (session.Result, bool) {
		if result, mine := proxied(command); mine {
			return result, true
		}
		switch {
		case strings.Contains(command, quoted("deploy")):
			posted = stood.held
			return session.Result{}, true
		default:
			return session.Result{}, false
		}
	}
	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	if !strings.Contains(posted, proxyDrainServer) || !strings.Contains(posted, retired) {
		t.Errorf("the config the helper was handed declares no drain server for the retired upstream:\n%s", posted)
	}
	deployed := stood.commands()[stood.at(quoted("deploy"))]
	for _, wanted := range []string{
		quoted("--target") + " " + quoted(flipTo),
		quoted("--retire") + " " + quoted(retired),
		quoted("--health-check-path") + " " + quoted("/healthz"),
		quoted("--deploy-timeout") + " " + quoted("30"),
		quoted("--drain-timeout") + " " + quoted("30"),
		quoted(ProxyHelperMount),
		quoted(ProxyContainer),
	} {
		if !strings.Contains(deployed, wanted) {
			t.Errorf("the one call is made as %q, which carries no %s", deployed, wanted)
		}
	}
}

func TestADrainThatExpiresIsWarnedAboutRatherThanFailed(t *testing.T) {
	t.Parallel()

	report := &watched{}
	_, err := released(t, aRelease(), session.Result{Stdout: caddyadmin.DrainExpired + " " + retired + " 2\n"}, report)
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

func TestAReleaseLeavesANeighboursDrainAloneAndWaitsForItBeforeStartingItsOwn(t *testing.T) {
	t.Parallel()

	neighbourDraining, err := RenderProxyConfig(ProxyState{
		Grace:    30 * time.Second,
		Routes:   []AppRoute{{RouteKey: keyed("web"), Upstream: retired}, {RouteKey: keyed("api"), Upstream: "prod-api-1:8080"}},
		Retiring: "prod-api-0",
	})
	if err != nil {
		t.Fatal(err)
	}
	neighbourDone, err := RenderProxyConfig(ProxyState{
		Grace:  30 * time.Second,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: retired}, {RouteKey: keyed("api"), Upstream: "prod-api-1:8080"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stood := &flipped{bench: machine(nil), held: string(neighbourDraining)}
	proxied := servesProxy(stood.bench, &stood.held)
	reads := 0
	stood.answer = func(command string) (session.Result, bool) {
		switch {
		case readsProxy(command):
			stood.mu.Lock()
			reads++
			if reads == 2 {
				stood.held = string(neighbourDone)
			}
			stood.mu.Unlock()
			return proxied(command)
		case strings.Contains(command, quoted("deploy")):
			return session.Result{}, true
		default:
			return proxied(command)
		}
	}

	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() while a neighbour drained = %v: the box drains one retired upstream at a time, so the flip waits for the neighbour's rather than taking its slot", err)
	}
	if reads < 3 {
		t.Errorf("the release read %s %d times, and a flip that found a neighbour draining had to read again after the wait", ProxyConfig, reads)
	}
	state, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if state.Retiring != "" {
		t.Errorf("the steady-state configuration still declares %s retiring", state.Retiring)
	}
	upstreams := map[string]string{}
	for _, route := range state.Routes {
		upstreams[route.App] = route.Upstream
	}
	if upstreams["web"] != flipTo || upstreams["api"] != "prod-api-1:8080" {
		t.Errorf("the box serves %v after the release, want web onto %s beside the neighbour's api", upstreams, flipTo)
	}
}

func TestASteadyStateWriteBesideANeighboursDrainKeepsThatDrainDeclared(t *testing.T) {
	t.Parallel()

	neighbourDraining, err := RenderProxyConfig(ProxyState{
		Grace:    30 * time.Second,
		Routes:   []AppRoute{{RouteKey: keyed("web"), Upstream: flipTo}, {RouteKey: keyed("api"), Upstream: "prod-api-1:8080"}},
		Retiring: "prod-api-0",
	})
	if err != nil {
		t.Fatal(err)
	}
	stood := &flipped{bench: machine(nil), held: configFor(t, flipTo)}
	proxied := servesProxy(stood.bench, &stood.held)
	writes := 0
	stood.answer = func(command string) (session.Result, bool) {
		switch {
		case writesProxy(command):
			stood.mu.Lock()
			writes++
			collided := writes == 2
			if collided {
				stood.held = string(neighbourDraining)
			}
			stood.mu.Unlock()
			if collided {
				return session.Result{Code: proxyMoved, Stderr: digested(string(neighbourDraining))}, true
			}
			return proxied(command)
		case strings.Contains(command, quoted("deploy")):
			return session.Result{}, true
		default:
			return proxied(command)
		}
	}
	rel := aRelease()
	rel.Retire = rel.Target

	if err := stood.host().Release(context.Background(), rel, nil); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	state, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if state.Retiring != "prod-api-0" {
		t.Errorf("the steady-state write left %q retiring, want the neighbour's prod-api-0 still declared: the drain server is the neighbour's to clear, and dropping it mid-drain leaves its helper counting an upstream the pool no longer names", state.Retiring)
	}
}

func TestAReleaseComposesItsRouteOntoWhatAConcurrentDeployLeftRatherThanRefusing(t *testing.T) {
	t.Parallel()

	neighbour, err := RenderProxyConfig(ProxyState{
		Grace:  30 * time.Second,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: retired}, {RouteKey: keyed("api"), Upstream: "prod-api-1:8080"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stood := &flipped{bench: machine(nil), held: configFor(t, retired)}
	proxied := servesProxy(stood.bench, &stood.held)
	writes := 0
	stood.answer = func(command string) (session.Result, bool) {
		switch {
		case writesProxy(command):
			stood.mu.Lock()
			writes++
			collided := writes == 1 || writes == 3
			if collided {
				stood.held = string(neighbour)
			}
			stood.mu.Unlock()
			if collided {
				return session.Result{Code: proxyMoved, Stderr: digested(string(neighbour))}, true
			}
			return proxied(command)
		case strings.Contains(command, quoted("deploy")):
			return session.Result{}, true
		default:
			return proxied(command)
		}
	}

	if err := stood.host().Release(context.Background(), aRelease(), nil); err != nil {
		t.Fatalf("Release() beside a deploy that rewrote %s twice = %v: the compare-and-set exists to refuse a lost update, not a neighbour", ProxyConfig, err)
	}
	state, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	upstreams := map[string]string{}
	for _, route := range state.Routes {
		upstreams[route.App] = route.Upstream
	}
	if upstreams["web"] != flipTo || upstreams["api"] != "prod-api-1:8080" {
		t.Errorf("the box serves %v after the release, want web onto %s beside the neighbour's api: the retry must compose onto what it re-read, not onto what it first read", upstreams, flipTo)
	}
	if state.Retiring != "" {
		t.Errorf("the steady-state configuration still declares %s retiring", state.Retiring)
	}
}

func TestAFailureAfterTheFlipSaysTheReleaseIsServingAndNamesWhatIsLeftBehind(t *testing.T) {
	t.Parallel()

	report := &watched{}
	stood := &flipped{bench: machine(nil), held: configFor(t, retired)}
	proxied := servesProxy(stood.bench, &stood.held)
	writes := 0
	stood.answer = func(command string) (session.Result, bool) {
		switch {
		case writesProxy(command):
			stood.mu.Lock()
			writes++
			steady := writes >= 2
			stood.mu.Unlock()
			if steady {
				return session.Result{Code: proxyMoved, Stderr: "the digest an ocel domain left"}, true
			}
			return proxied(command)
		case strings.Contains(command, quoted("deploy")):
			return session.Result{Stdout: caddyadmin.DrainExpired + " " + retired + " 2\n"}, true
		default:
			return proxied(command)
		}
	}

	err := stood.host().Release(context.Background(), aRelease(), report)
	if err == nil {
		t.Fatal("the steady-state write was refused and the release reported success")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a failure after the flip failed with %T (%v), want the refusal every other failure renders: a bare machine error reads as a failed release while the new one is in fact serving", err, err)
	}
	said := err.Error()
	for what, wanted := range map[string]string{
		"that the flip already landed":      "flipped",
		"the release that is now serving":   physical,
		"the drain server left declared":    proxyDrainServer,
		"the stopped container it names":    retiring,
		"the file a restarted proxy reads":  ProxyConfig,
		"why the steady-state write failed": "digest",
	} {
		if !strings.Contains(said, wanted) {
			t.Errorf("a failure after the flip is refused with\n%s\nand that names no %s (%s)", said, what, wanted)
		}
	}
	warned := strings.Join(report.told, "\n")
	if !strings.Contains(warned, retired) || !strings.Contains(warned, "502") {
		t.Errorf("the release reported %q; the drain expired holding requests open and the write that failed after the flip swallowed the warning", warned)
	}
}

func diagnosed(t *testing.T, helper session.Result, state, logs string) string {
	t.Helper()
	stood := &flipped{bench: machine(nil), held: configFor(t, retired)}
	proxied := servesProxy(stood.bench, &stood.held)
	stood.answer = func(command string) (session.Result, bool) {
		if result, mine := proxied(command); mine {
			return result, true
		}
		switch {
		case strings.Contains(command, quoted("deploy")):
			return helper, true
		case strings.Contains(command, "docker inspect") && strings.Contains(command, ".State."):
			return session.Result{Stdout: state}, true
		case strings.Contains(command, "docker logs"):
			return session.Result{Stdout: logs}, true
		default:
			return session.Result{}, false
		}
	}
	err := stood.host().Release(context.Background(), aRelease(), nil)
	if err == nil {
		t.Fatal("a release the helper refused returned no error at all")
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
		session.Result{Code: 4, Stderr: physical + ":" + AppPort + " never answered /healthz within 30s"},
		"Status=running ExitCode=0 OOMKilled=false Error= StartedAt=2026-01-01T00:00:00Z FinishedAt=0001-01-01T00:00:00Z RestartCount=0", "")

	for what, wanted := range map[string]string{
		"the verdict the helper reached":      "never answered",
		"the exact target it probed":          physical + ":" + AppPort,
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

func refusedAfter(t *testing.T, flip session.Result) string {
	t.Helper()
	stood := &flipped{bench: machine(nil), held: configFor(t, retired)}
	proxied := servesProxy(stood.bench, &stood.held)
	stood.answer = func(command string) (session.Result, bool) {
		if result, mine := proxied(command); mine {
			return result, true
		}
		switch {
		case strings.Contains(command, quoted("deploy")):
			return session.Result{Code: 5, Stderr: "carries no upstream " + retired}, true
		case strings.Contains(command, quoted("flip")):
			return flip, true
		default:
			return session.Result{}, false
		}
	}
	err := stood.host().Release(context.Background(), aRelease(), nil)
	if err == nil {
		t.Fatal("a release the helper refused returned no error at all")
	}
	return err.Error()
}

func TestARefusalNamesTheLiveUpstreamOnceAndTheAnswerFollowsWhetherTheProxyWasPutBack(t *testing.T) {
	t.Parallel()

	rolled := refusedAfter(t, session.Result{})
	if !strings.Contains(rolled, "the previous release is still the live upstream") {
		t.Errorf("a refusal that put the proxy back reads\n%s\nand never says which release is live", rolled)
	}
	stranded := refusedAfter(t, session.Result{Code: 1, Stderr: "the proxy answered nothing over /run/caddy-admin.sock"})
	if strings.Contains(stranded, "the previous release is still the live upstream") {
		t.Errorf("a refusal that could not put the proxy back reads\n%s\nwhich asserts the previous release is live and then corrects itself further down, and a reader meets the false half first", stranded)
	}
	if !strings.Contains(stranded, physical) {
		t.Errorf("a refusal that could not put the proxy back reads\n%s\nand never names the container it left standing", stranded)
	}
}

func strandedByWrite(t *testing.T, back session.Result) (*flipped, string) {
	t.Helper()
	stood := &flipped{bench: machine(nil), held: configFor(t, retired)}
	writes := 0
	proxied := servesProxy(stood.bench, &stood.held)
	stood.answer = func(command string) (session.Result, bool) {
		if !writesProxy(command) {
			return proxied(command)
		}
		stood.mu.Lock()
		writes++
		first := writes == 1
		stood.mu.Unlock()
		if first {
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
	return stood, err.Error()
}

func TestAFlipConfigurationThatCannotBeWrittenLeavesNeitherAStandingContainerNorAHalfWrittenFile(t *testing.T) {
	t.Parallel()

	stood, said := strandedByWrite(t, session.Result{})
	if stood.at(quoted("deploy")) >= 0 {
		t.Errorf("a flip configuration that was never written still reached the helper: %v", stood.commands())
	}
	if stood.at("docker rm --force "+quoted(physical)) < 0 {
		t.Errorf("a flip configuration that could not be written left %s standing with nothing routing to it: %v", physical, stood.commands())
	}
	state, err := ReadProxyState([]byte(stood.held))
	if err != nil {
		t.Fatalf("%s was left as %q, which a restarted proxy reads as its serving configuration: %v", ProxyConfig, stood.held, err)
	}
	if len(state.Routes) != 1 || state.Routes[0].Upstream != retired {
		t.Errorf("%s was left naming %v rather than the upstream that is still live", ProxyConfig, state.Routes)
	}
	for what, wanted := range map[string]string{
		"the file it could not write":  ProxyConfig,
		"why the write failed":         "no space left on device",
		"what became of the container": physical,
	} {
		if !strings.Contains(said, wanted) {
			t.Errorf("a flip configuration that could not be written is refused with\n%s\nand that names no %s (%s)", said, what, wanted)
		}
	}
}

func TestAFlipConfigurationThatCannotBeWrittenBackEitherNamesTheFileARestartWouldServe(t *testing.T) {
	t.Parallel()

	stood, said := strandedByWrite(t, session.Result{Code: 1, Stderr: "no space left on device"})
	if !strings.Contains(said, "could not be put back") || !strings.Contains(said, ProxyConfig) {
		t.Errorf("a %s that could be neither written nor put back is refused with\n%s\nwhich never says which file a restarted proxy would read", ProxyConfig, said)
	}
	if _, err := ReadProxyState([]byte(stood.held)); err != nil {
		t.Errorf("%s was left as %q, which no restarted proxy could read: the write stages beside the file and moves it into place, so a write that fails leaves the file whole", ProxyConfig, stood.held)
	}
}

func TestTheDrainContractIsStatedOnEveryReleaseThatRetiresSomething(t *testing.T) {
	t.Parallel()

	report := &watched{}
	if _, err := released(t, aRelease(), session.Result{}, report); err != nil {
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

	first := aRelease()
	first.Retire = ""
	quiet := &watched{}
	if _, err := released(t, first, session.Result{}, quiet); err != nil {
		t.Fatal(err)
	}
	if len(quiet.told) != 0 {
		t.Errorf("a first deploy states a drain contract for a container it does not have: %v", quiet.told)
	}
}
