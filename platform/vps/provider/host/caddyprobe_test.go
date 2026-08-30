package host

import (
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

func probingConfig(t *testing.T, rendered []byte) func(hostname string) answered {
	t.Helper()

	engineOrSkip(t)
	dir := seenByTheEngine(t)
	config := filepath.Join(dir, "caddy.json")
	if err := os.WriteFile(config, rendered, 0o644); err != nil {
		t.Fatal(err)
	}

	name := probeName(t)
	exec.Command(dockerEngine, "rm", "--force", name).Run()
	stood, err := exec.Command(dockerEngine, "run", "--rm", "--detach", "--name", name,
		"--publish", "127.0.0.1::80",
		"--volume", config+":"+proxyConfigMount+":ro",
		ProxyImage, "caddy", "run", "--config", proxyConfigMount).CombinedOutput()
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
	return "ocel-probe-" + strings.ToLower(strings.NewReplacer("/", "-", " ", "-").Replace(t.Name()))
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
