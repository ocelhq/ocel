package host

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type answered struct {
	status int
	edge   string
	body   string
}

func probing(t *testing.T, state ProxyState) func(hostname string) answered {
	t.Helper()

	rendered, err := RenderProxyConfig(state)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	return probingConfig(t, rendered)
}

func probingConfig(t *testing.T, rendered []byte, joined ...string) func(hostname string) answered {
	t.Helper()

	engineOrSkip(t)
	dir := seenByTheEngine(t)
	config := filepath.Join(dir, "caddy.json")
	if err := os.WriteFile(config, rendered, 0o644); err != nil {
		t.Fatal(err)
	}

	name := probeName(t)
	exec.Command(dockerEngine, "rm", "--force", name).Run()
	run := []string{"run", "--rm", "--detach", "--name", name,
		"--publish", "127.0.0.1::80",
		"--volume", config + ":" + proxyConfigMount + ":ro"}
	for _, network := range joined {
		run = append(run, "--network", network)
	}
	run = append(run, ProxyImage, "caddy", "run", "--config", proxyConfigMount)
	stood, err := exec.Command(dockerEngine, run...).CombinedOutput()
	if err != nil {
		t.Skipf("this machine's engine will not run %s: %s", ProxyImage, stood)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "rm", "--force", name).Run() })

	published, err := exec.Command(dockerEngine, "port", name, "80/tcp").Output()
	if err != nil {
		t.Fatalf("read the port the probe proxy publishes: %v", err)
	}
	at := "http://" + strings.TrimSpace(strings.Split(string(published), "\n")[0])

	ask := func(hostname string) answered {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, at+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		if hostname != "" {
			request.Host = hostname
		}
		said, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("ask the running proxy for %q: %v\n%s", hostname, err,
				strings.TrimSpace(logsOf(name)))
		}
		defer said.Body.Close()
		body, err := io.ReadAll(said.Body)
		if err != nil {
			t.Fatal(err)
		}
		return answered{status: said.StatusCode, edge: said.Header.Get(EdgeHeader), body: string(body)}
	}

	standing := false
	for range 100 {
		request, _ := http.NewRequest(http.MethodGet, at+"/", nil)
		if said, err := http.DefaultClient.Do(request); err == nil {
			said.Body.Close()
			standing = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !standing {
		t.Fatalf("the probe proxy never answered on %s:\n%s", at, logsOf(name))
	}
	return ask
}

func probeName(t *testing.T) string {
	readable := strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))
	sum := sha256.Sum256([]byte(t.Name()))
	return "ocel-probe-" + readable[:min(len(readable), 28)] + "-" + hex.EncodeToString(sum[:4])
}

func logsOf(name string) string {
	said, _ := exec.Command(dockerEngine, "logs", "--tail", "40", name).CombinedOutput()
	return string(said)
}

func TestARealProxyAnswersAHostnameNothingOnTheBoxClaimsWithABare404(t *testing.T) {
	for _, box := range []struct {
		what  string
		state ProxyState
	}{
		{"a box serving nothing", ProxyState{Grace: DrainWindow}},
		{"a box serving one project", routed()},
		{"a box serving two projects", twoProjects()},
	} {
		t.Run(box.what, func(t *testing.T) {
			said := probing(t, box.state)("unclaimed.example.com")
			if said.status != http.StatusNotFound {
				t.Errorf("%s answers a hostname nothing claims with %d, want 404: an empty 200 reads as healthy to everything that checks, so a box serving nobody and a box serving everybody look alike",
					box.what, said.status)
			}
			if said.edge != EdgeName {
				t.Errorf("%s answers with %s: %q, want %q: the refusal names the edge that made it and nothing else on the machine",
					box.what, EdgeHeader, said.edge, EdgeName)
			}
			if said.body != "" {
				t.Errorf("%s answers with a body of %q, want nothing: an unclaimed hostname is told nothing about what else this box serves", box.what, said.body)
			}
		})
	}
}

func TestARealProxyForwardsAClaimedHostnameToTheProjectThatClaimedIt(t *testing.T) {
	state := twoProjects()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}}
	ask := probing(t, state)

	if said := ask(claimed); said.status == http.StatusNotFound {
		t.Errorf("the claimed hostname %q was answered %d by the box's own default, want the route of the surface that claimed it: the default stands behind every route ocel writes and never in front of one", claimed, said.status)
	} else if said.edge != EdgeName {
		t.Errorf("the surface's route answered %q with %s: %q, want %q: the probe a bind waits on reads this header off the hostname itself, so a route that forwards without naming the edge leaves every hostname the box actually serves reported as served by nothing", claimed, EdgeHeader, said.edge, EdgeName)
	}
	if said := ask("blog.example.com"); said.status != http.StatusNotFound || said.edge != EdgeName {
		t.Errorf("a hostname the other project never claimed was answered %d by %q, want the box's own 404", said.status, said.edge)
	}
}

func TestARealProxyAnswersEveryHostnameOneSurfaceClaimsOnTheAppItRuns(t *testing.T) {
	second := "second.example.com"
	state := routed()
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface}, {Hostname: second, Owner: surface}}
	ask := probing(t, state)

	for _, hostname := range []string{claimed, second} {
		said := ask(hostname)
		if said.status == http.StatusNotFound {
			t.Errorf("%q was answered %d by the box's own default, want the one app its surface runs: a project binds a second domain without giving up the first, and the route this renders carries every hostname the surface claims", hostname, said.status)
			continue
		}
		if said.edge != EdgeName {
			t.Errorf("%q is served by the surface's own route and answers %s: %q, want %q: every response this box emits names the box, or the bind's probe reads the app's answer as nobody's", hostname, EdgeHeader, said.edge, EdgeName)
		}
	}
}

func standingApp(t *testing.T, body string) (network, upstream string) {
	t.Helper()

	engineOrSkip(t)
	network, name := probeName(t)+"-net", probeName(t)+"-app"
	exec.Command(dockerEngine, "network", "rm", network).Run()
	if out, err := exec.Command(dockerEngine, "network", "create", network).CombinedOutput(); err != nil {
		t.Skipf("this machine's engine will not create a network for the app the proxy forwards to: %s", out)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "network", "rm", network).Run() })

	exec.Command(dockerEngine, "rm", "--force", name).Run()
	stood, err := exec.Command(dockerEngine, "run", "--rm", "--detach", "--name", name,
		"--network", network, ProxyImage,
		"caddy", "respond", "--listen", ":"+AppPort, body).CombinedOutput()
	if err != nil {
		t.Skipf("this machine's engine will not run the app the proxy forwards to: %s", stood)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "rm", "--force", name).Run() })
	return network, name + ":" + AppPort
}

func TestARealProxyServesTheAppsBodyUnderTheHostnameAndNamesTheEdgeThatServedIt(t *testing.T) {
	network, upstream := standingApp(t, "the app answered")

	state := ProxyState{
		Grace:  DrainWindow,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: upstream}},
		Claims: []HostClaim{{Hostname: claimed, Owner: surface}},
	}
	said := probingConfig(t, issuedByNobody(t, mustRender(t, state)), network)(claimed)

	if said.status != http.StatusOK || said.body != "the app answered" {
		t.Errorf("the bound hostname was answered %d %q, want the body of the app its surface runs", said.status, said.body)
	}
	if said.edge != EdgeName {
		t.Errorf("the bound hostname was answered with %s: %q, want %q. The settle reads that header off the answer to decide which edge serves a hostname, so a forwarded route that names no edge leaves `ocel domain add` waiting on a box that is already serving",
			EdgeHeader, said.edge, EdgeName)
	}
}

func TestARealProxyStopsServingAHostnameTheProjectUnbound(t *testing.T) {
	network, upstream := standingApp(t, "the app answered")

	bound := ProxyState{
		Grace:  DrainWindow,
		Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: upstream}},
		Claims: []HostClaim{{Hostname: claimed, Owner: surface}},
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
		if said := probingConfig(t, issuedByNobody(t, mustRender(t, bound)), network)(claimed); said.body != "the app answered" {
			t.Fatalf("the hostname answered %d %q while bound, want the app's body", said.status, said.body)
		}
	})
	t.Run("unbound", func(t *testing.T) {
		said := probingConfig(t, issuedByNobody(t, rendered), network)(claimed)
		if said.status != http.StatusNotFound || said.edge != EdgeName || said.body != "" {
			t.Errorf("the unbound hostname was answered %d %q by %q, want the box's own bare 404: an unbind that leaves the route matching keeps serving a site the project gave back",
				said.status, said.body, said.edge)
		}
	})
}
