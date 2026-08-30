package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
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
		App:           "web",
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
		Routes: []AppRoute{{App: "web", Upstream: upstream}},
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
	stood := &flipped{bench: machine(nil), held: configFor(t, retired)}
	stood.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.HasPrefix(command, "cat "+quoted(ProxyConfig)):
			return session.Result{Stdout: stood.held}, true
		case strings.Contains(command, "cat > "+quoted(ProxyConfig)):
			stood.mu.Lock()
			stood.held = stood.fed[len(stood.fed)-1]
			stood.mu.Unlock()
			return session.Result{}, true
		case strings.Contains(command, quoted("deploy")):
			return helper, true
		default:
			return session.Result{}, false
		}
	}
	return stood, stood.host().Release(context.Background(), rel, report)
}

func (f *flipped) at(fragment string) int {
	for at, command := range f.commands() {
		if strings.Contains(command, fragment) {
			return at
		}
	}
	return -1
}

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
	stood.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.HasPrefix(command, "cat "+quoted(ProxyConfig)):
			return session.Result{Stdout: stood.held}, true
		case strings.Contains(command, "cat > "+quoted(ProxyConfig)):
			stood.mu.Lock()
			stood.held = stood.fed[len(stood.fed)-1]
			stood.mu.Unlock()
			return session.Result{}, true
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
	_, err := released(t, aRelease(), session.Result{Stdout: drainExpired + " " + retired + " 2\n"}, report)
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

func diagnosed(t *testing.T, helper session.Result, state, logs string) string {
	t.Helper()
	stood := &flipped{bench: machine(nil), held: configFor(t, retired)}
	stood.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.HasPrefix(command, "cat "+quoted(ProxyConfig)):
			return session.Result{Stdout: stood.held}, true
		case strings.Contains(command, "cat > "+quoted(ProxyConfig)):
			return session.Result{}, true
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
		if !strings.Contains(said, "Status="+strings.TrimSuffix(what, "hung")) && what != "hung" {
			t.Errorf("%s is refused with\n%s\nand never names the status it was found in", what, said)
		}
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
