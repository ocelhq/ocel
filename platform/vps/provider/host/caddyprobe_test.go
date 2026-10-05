package host

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/provider/enginetest"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type answered struct {
	status   int
	router   router.Kind
	body     string
	location string
}

func probing(t *testing.T, state RoutingTable) func(hostname string) answered {
	t.Helper()
	return probingConfig(t, state, mustRender(t, state))
}

func probingConfig(t *testing.T, state RoutingTable, rendered []byte, joined ...string) func(hostname string) answered {
	t.Helper()
	_, ask := probedBox(t, state, rendered, joined...)
	return ask
}

func probedBox(t *testing.T, state RoutingTable, rendered []byte, joined ...string) (liveProxy, func(hostname string) answered) {
	t.Helper()

	proxy := aLiveProxy(t)
	for _, network := range joined {
		if out, err := exec.Command(dockerEngine, "network", "connect", network, proxy.board).CombinedOutput(); err != nil && !strings.Contains(string(out), "already exists") {
			t.Fatalf("put the switchboard on %s: %v\n%s", network, err, out)
		}
	}
	proxy.stages(t, routingTableItem().Content, state, rendered)
	proxy.drives(t, "load", proxy.table)
	proxy.reloads(t)
	at := "http://127.0.0.1:" + caddy.HTTPPort

	return proxy, func(hostname string) answered {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, at+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if hostname != "" {
			request.Host = hostname
		}
		asking := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		said, err := asking.Do(request)
		if err != nil {
			t.Fatalf("ask the running proxy for %q: %v\n%s\n%s", hostname, err,
				strings.TrimSpace(logsOf(proxy.name)), strings.TrimSpace(logsOf(proxy.board)))
		}
		defer said.Body.Close()
		body, err := io.ReadAll(said.Body)
		if err != nil {
			t.Fatal(err)
		}
		return answered{status: said.StatusCode, router: router.Kind(said.Header.Get(router.HeaderRouter)), body: string(body), location: said.Header.Get("Location")}
	}
}

var probeRun = rand.Text()

func probeName(t *testing.T) string {
	readable := strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))
	sum := sha256.Sum256([]byte(probeRun + t.Name()))
	return "ocel-probe-" + readable[:min(len(readable), 28)] + "-" + hex.EncodeToString(sum[:4])
}

func logsOf(name string) string {
	said, _ := exec.Command(dockerEngine, "logs", "--tail", "40", name).CombinedOutput()
	return string(said)
}

func TestARealProxyAnswersAHostnameNothingOnTheBoxClaimsWithABare404(t *testing.T) {
	for _, box := range []struct {
		what  string
		state RoutingTable
	}{
		{"a box serving nothing", RoutingTable{Grace: DrainWindow}},
		{"a box serving one project", routed()},
		{"a box serving two projects", twoProjects()},
	} {
		t.Run(box.what, func(t *testing.T) {
			said := probing(t, box.state)("unclaimed.example.com")
			if said.status != http.StatusNotFound {
				t.Errorf("%s answers a hostname nothing claims with %d, want 404: an empty 200 reads as healthy to everything that checks, so a box serving nobody and a box serving everybody look alike",
					box.what, said.status)
			}
			if said.router != switchboard.RouterKind {
				t.Errorf("%s answers with %s: %q, want %q: the refusal names the router that made it and nothing else on the machine",
					box.what, router.HeaderRouter, said.router, switchboard.RouterKind)
			}
			if said.body != "" {
				t.Errorf("%s answers with a body of %q, want nothing: an unclaimed hostname is told nothing about what else this box serves", box.what, said.body)
			}
		})
	}
}

func TestARealProxyForwardsAClaimedHostnameToTheProjectThatClaimedIt(t *testing.T) {
	state := twoProjects()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	ask := probing(t, state)

	if said := ask(claimed); said.status == http.StatusNotFound {
		t.Errorf("the claimed hostname %q was answered %d by the box's own default, want the route of the surface that claimed it: the default sits behind every route ocel writes and never in front of one", claimed, said.status)
	} else if said.router != switchboard.RouterKind {
		t.Errorf("the surface's route answered %q with %s: %q, want %q: the probe a bind waits on reads this header off the hostname itself, so a route that forwards without naming the router leaves every hostname the box actually serves reported as served by nothing", claimed, router.HeaderRouter, said.router, switchboard.RouterKind)
	}
	if said := ask("blog.example.com"); said.status != http.StatusNotFound || said.router != switchboard.RouterKind {
		t.Errorf("a hostname the other project never claimed was answered %d by %q, want the box's own 404", said.status, said.router)
	}
}

func TestARealProxyAnswersEveryHostnameOneSurfaceClaimsOnTheAppItRuns(t *testing.T) {
	second := "second.example.com"
	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}, {Hostname: second, Owner: surface, Pointer: pointed}}
	ask := probing(t, state)

	for _, hostname := range []string{claimed, second} {
		said := ask(hostname)
		if said.status == http.StatusNotFound {
			t.Errorf("%q was answered %d by the box's own default, want the one app its surface runs: a project binds a second domain without giving up the first, and the route this renders names every hostname the surface claims", hostname, said.status)
			continue
		}
		if said.router != switchboard.RouterKind {
			t.Errorf("%q is served by the surface's own route and answers %s: %q, want %q: every response this box emits names the box, or the bind's probe reads the app's answer as nobody's", hostname, router.HeaderRouter, said.router, switchboard.RouterKind)
		}
	}
}

func appOn(t *testing.T, network, named, body string) string {
	t.Helper()

	name := probeName(t) + "-" + named
	exec.Command(dockerEngine, "rm", "--force", name).Run()
	run := append([]string{"run", "--rm", "--detach", "--name", name}, enginetest.RunLabelArgs(t)...)
	out, err := exec.Command(dockerEngine, append(run, "--network", network, caddy.Image(),
		"caddy", "respond", "--listen", ":"+containerimage.PortText, body)...).CombinedOutput()
	if err != nil {
		t.Skipf("this machine's engine will not run the app the proxy forwards to: %s", out)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "rm", "--force", name).Run() })
	return name + ":" + containerimage.PortText
}

func anApp(t *testing.T, body string) (network, upstream string) {
	t.Helper()

	network = enginetest.Network(t, "box")
	return network, appOn(t, network, "app", body)
}

func TestARealProxyServesTheAppsBodyUnderTheHostnameAndNamesTheRouterThatServedIt(t *testing.T) {
	network, upstream := anApp(t, "the app answered")

	state := RoutingTable{
		Grace:  DrainWindow,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: upstream}},
		Claims: []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}},
	}
	said := probingConfig(t, state, issuedByNobody(t, mustRender(t, state)), network)(claimed)

	if said.status != http.StatusOK || said.body != "the app answered" {
		t.Errorf("the bound hostname was answered %d %q, want the body of the app its surface runs", said.status, said.body)
	}
	if said.router != switchboard.RouterKind {
		t.Errorf("the bound hostname was answered with %s: %q, want %q. The domain check reads that header off the answer to decide which edge serves a hostname, so a forwarded route that names no edge leaves `ocel domain add` waiting on a box that is already serving",
			router.HeaderRouter, said.router, switchboard.RouterKind)
	}
}

func TestARealProxyStopsServingAHostnameTheProjectUnbound(t *testing.T) {
	network, upstream := anApp(t, "the app answered")

	bound := RoutingTable{
		Grace:  DrainWindow,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: upstream}},
		Claims: []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}},
	}
	unbound := bound
	unbound.Claims = Disclaiming(bound.Claims, func(claim HostClaim) bool {
		return claim.Hostname == claimed && claim.Owner == surface
	})

	rendered := mustRender(t, unbound)
	if strings.Contains(string(rendered), claimed) {
		t.Errorf("the configuration an unbind renders still names %s:\n%s", claimed, rendered)
	}

	t.Run("bound", func(t *testing.T) {
		if said := probingConfig(t, bound, issuedByNobody(t, mustRender(t, bound)), network)(claimed); said.body != "the app answered" {
			t.Fatalf("the hostname answered %d %q while bound, want the app's body", said.status, said.body)
		}
	})
	t.Run("unbound", func(t *testing.T) {
		said := probingConfig(t, unbound, issuedByNobody(t, rendered), network)(claimed)
		if said.status != http.StatusNotFound || said.router != switchboard.RouterKind || said.body != "" {
			t.Errorf("the unbound hostname was answered %d %q by %q, want the box's own bare 404: an unbind that leaves the route matching keeps serving a site the project gave back",
				said.status, said.body, said.router)
		}
	})
}
