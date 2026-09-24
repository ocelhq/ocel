package host

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/enginetest"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func TestMain(m *testing.M) { os.Exit(enginetest.Main(m)) }

func engineOrSkip(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath(dockerEngine); err != nil {
		t.Skip("this machine carries no docker, and what an engine reports cannot be read off one that is not here")
	}
	if err := exec.Command(dockerEngine, "info").Run(); err != nil {
		t.Skip("the docker on this machine answers nothing, so there is no engine to measure against")
	}
	for _, held := range []struct{ kind, name, take string }{
		{"container", ProxyContainer, "docker rm --force " + ProxyContainer},
		{"network", ProxyNetwork, "docker network rm " + ProxyNetwork},
	} {
		if exec.Command(dockerEngine, held.kind, "inspect", held.name).Run() == nil {
			t.Fatalf("this machine already carries the %s %q ocel writes, and the test will not take something it did not create. Skipping here is how the redirect ordering, the subject collection and the pinned pair evaporate into a green run under stale local state. Take it with `%s` and re-run",
				held.kind, held.name, held.take)
		}
	}
}

type standingProxy struct {
	name    string
	network string
	dir     string
	pins    string
	helper  string
	here    func(string) string
}

func proxyStanding(t *testing.T) standingProxy {
	t.Helper()

	engineOrSkip(t)
	name, network := probeName(t), enginetest.Network(t)
	t.Cleanup(func() { taken(t, name) })
	dir := enginetest.BindSource(t)

	arch, err := Architecture(runtime.GOARCH)
	if err != nil {
		t.Skipf("no flip helper is built for a machine reporting %q", runtime.GOARCH)
	}
	config, helper := filepath.Join(dir, proxyConfigName), filepath.Join(dir, proxyHelperName)
	table := filepath.Join(dir, "routing.json")
	for path, seeded := range map[string][]byte{config: proxyConfigItem().Content, table: routingTableItem().Content} {
		if err := os.WriteFile(path, seeded, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	runnable(t, helper, proxyHelper(arch), 0o750)
	pins := filepath.Join(dir, "pins")
	for _, made := range []string{filepath.Join(dir, "data"), pins} {
		if err := os.MkdirAll(made, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stood := standingProxy{name: name, network: network, dir: dir, pins: pins, helper: helper, here: func(written string) string {
		return strings.NewReplacer(ProxyPins, pins, proxyRoot, dir, live.RoutingTable, table, ProxyHelper, helper,
			quoted(ProxyContainer), quoted(name), quoted(ProxyNetwork), quoted(network), `"`+ProxyNetwork+`"`, `"`+network+`"`, routingLock, dir).Replace(written)
	}}

	taken(t, name)
	if out, err := exec.Command("/bin/sh", "-c", stood.here(containerCommand())).CombinedOutput(); err != nil {
		t.Fatalf("the write that stands the proxy up = %v\n%s", err, out)
	}
	return stood
}

func taken(t *testing.T, name string) {
	t.Helper()

	exec.Command(dockerEngine, "rm", "--force", name).Run()
	for range 100 {
		if exec.Command(dockerEngine, "container", "inspect", name).Run() != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Errorf("%s is still reported by the engine after a forced removal, and the ports and the name it holds are the next test's to take", name)
}

func TestTheProbeReadsARealEngineExactlyAsTheItemStatesIt(t *testing.T) {
	stood := proxyStanding(t)
	dir, helper := stood.dir, stood.helper

	rendered, err := exec.Command("/bin/sh", "-c", stood.here(containerProbe())).Output()
	if err != nil {
		t.Fatalf("probe the proxy this machine is running: %v", err)
	}
	observed, _, err := readSurvey(string(rendered))
	if err != nil {
		t.Fatal(err)
	}

	stated := containerItem()
	stated.Name = stood.name
	stated.Content = proxyFactsOver([]string{
		dir + ":" + proxyConfigDir + ":ro",
		helper + ":" + ProxyHelperMount + ":ro",
		stood.pins + ":" + proxyPinsMount + ":ro",
		filepath.Join(dir, "data") + ":" + proxyDataMount,
		ConnectorRun + ":" + ConnectorRun + ":ro",
	})
	if observed[stated.ID()] != stated.Digest() {
		box, _ := exec.Command(dockerEngine, "inspect", "--type", "container", "--format", ProxyFactTemplate, stood.name).Output()
		t.Errorf("a real engine reports the proxy as something other than the item ocel writes it from, so every re-run plans an update over a proxy that stands:\n%s",
			compared(canonical(string(box)), strings.TrimSpace(string(stated.Content))))
	}
}

func canonical(rendered string) string {
	lines := strings.Split(strings.TrimSpace(rendered), "\n")
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

func compared(box, stated string) string {
	said, want := strings.Split(box, "\n"), strings.Split(stated, "\n")
	var written strings.Builder
	for at := 0; at < max(len(said), len(want)); at++ {
		var read, meant string
		if at < len(said) {
			read = said[at]
		}
		if at < len(want) {
			meant = want[at]
		}
		mark := "  "
		if read != meant {
			mark = "! "
		}
		written.WriteString(mark + "box:  " + read + "\n" + mark + "item: " + meant + "\n")
	}
	return written.String()
}

func (p standingProxy) drives(t *testing.T, argv ...string) string {
	t.Helper()

	run := exec.Command(dockerEngine, append([]string{"exec", p.name, ProxyHelperMount}, argv...)...)
	said, err := run.Output()
	if err != nil {
		var stderr string
		exited := &exec.ExitError{}
		if errors.As(err, &exited) {
			stderr = string(exited.Stderr)
		}
		t.Fatalf("%v against the running proxy = %v\n%s", argv, err, stderr)
	}
	return string(said)
}

func (p standingProxy) standsApp(t *testing.T, upstream, body string) {
	t.Helper()

	name, _, _ := strings.Cut(upstream, ":")
	exec.Command(dockerEngine, "rm", "--force", name).Run()
	run := append([]string{"run", "--rm", "--detach", "--name", name}, enginetest.Labelled(t)...)
	stood, err := exec.Command(dockerEngine, append(run, "--network", p.network, ProxyImage,
		"caddy", "respond", "--listen", ":"+providerkit.InjectedPortText, body)...).CombinedOutput()
	if err != nil {
		t.Skipf("this machine's engine will not run the app the proxy forwards to: %s", stood)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "rm", "--force", name).Run() })
}

func TestAConfigMovedIntoPlaceIsWhatTheRunningProxyLoads(t *testing.T) {
	stood := proxyStanding(t)

	flipped := routed()
	stood.standsApp(t, flipped.Routes[0].Upstream, "the app answered")
	flipped.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood.writes(t, routingTableItem().Content, flipped)

	if read := stood.drives(t, "flip", ProxyConfigMount); strings.TrimSpace(read) != "" {
		t.Logf("the flip said %q", strings.TrimSpace(read))
	}

	upstream, route := flipped.Routes[0].Upstream, flipped.Routes[0].identity()
	if held := stood.drives(t, "config", "apps/http/servers/"+proxyServer+"/routes"); !strings.Contains(held, route) {
		t.Fatalf("the running proxy holds\n%s\nafter a deploy moved a config naming %s into place: the deploy writes %s by staging beside it and renaming, and a proxy handed that file through a bind of the file itself keeps reading the inode it was started on and reloads whatever it was seeded with",
			strings.TrimSpace(held), route, ProxyConfig)
	}

	ask := func(hostname string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+proxyPort+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = hostname
		said, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("ask the proxy this machine is running for %q: %v", hostname, err)
		}
		t.Cleanup(func() { said.Body.Close() })
		return said
	}

	if said := ask("unclaimed.example.com"); said.StatusCode != http.StatusNotFound || said.Header.Get(EdgeHeader) != EdgeName {
		t.Errorf("a hostname nothing on this box claims was answered %d carrying %s: %q, want a 404 naming this edge: an empty 200 is what the seeded config answers everything with, and a proxy still serving the seed after a deploy looks healthy to everything that checks it",
			said.StatusCode, EdgeHeader, said.Header.Get(EdgeHeader))
	}
	said := ask(claimed)
	read, err := io.ReadAll(io.LimitReader(said.Body, 1<<12))
	if err != nil {
		t.Fatalf("read what the proxy answered for %q: %v", claimed, err)
	}
	if said.StatusCode != http.StatusOK || string(read) != "the app answered" {
		t.Errorf("the hostname %q claims was answered %d %q, want the body of the app standing on %s: a route that reaches no upstream answers a 502 that proves only the shape of the failure, and the box's own default answers a 404, so neither says the deploy loaded the document naming that upstream",
			surface, said.StatusCode, read, upstream)
	}
	if said.Header.Get(EdgeHeader) != EdgeName {
		t.Errorf("the surface's own route answered %s: %q, want %q: the bind's probe reads this header off the hostname itself and a route that forwards without it reports the box as serving nothing",
			EdgeHeader, said.Header.Get(EdgeHeader), EdgeName)
	}
}

func (p standingProxy) stages(t *testing.T, held []byte, state RoutingTable, config []byte) []byte {
	t.Helper()

	written := mustWrite(t, state)
	write := exec.Command("/bin/sh", "-c", p.here(stagedWrite(tableDigest(contentSum(held)))))
	write.Stdin = strings.NewReader(pairFed(routingPair{table: written, config: config}))
	if out, err := write.CombinedOutput(); err != nil {
		t.Fatalf("the staged write a deploy makes = %v\n%s", err, out)
	}
	return written
}

func (p standingProxy) writes(t *testing.T, held []byte, state RoutingTable) []byte {
	t.Helper()
	return p.stages(t, held, state, mustRender(t, state))
}

func (p standingProxy) moves(t *testing.T, held []byte, state RoutingTable) []byte {
	t.Helper()

	written := p.writes(t, held, state)
	p.drives(t, "flip", ProxyConfigMount)
	return written
}

func (p standingProxy) standsSlowApp(t *testing.T, upstream string, slow time.Duration) {
	t.Helper()

	name, _, _ := strings.Cut(upstream, ":")
	exec.Command(dockerEngine, "rm", "--force", name).Run()
	answer := fmt.Sprintf(`while read -r line && [ "$line" != "$(printf '\r')" ]; do :; done; sleep %d; printf 'HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n'`,
		int(slow.Seconds()))
	stood, err := exec.Command(dockerEngine, "run", "--rm", "--detach", "--name", name,
		"--network", p.network, "--entrypoint", "nc", ProxyImage,
		"-lk", "-p", providerkit.InjectedPortText, "-e", "sh", "-c", answer).CombinedOutput()
	if err != nil {
		t.Skipf("this machine's engine will not run the app the proxy forwards to: %s", stood)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "rm", "--force", name).Run() })
}

func TestARealProxyDropsNoRequestWhileAFlipMovesAStandingRouteBetweenUpstreams(t *testing.T) {
	stood := proxyStanding(t)

	one, two := "shop-web-1111:"+providerkit.InjectedPortText, "shop-web-2222:"+providerkit.InjectedPortText
	stood.standsApp(t, one, "one")
	stood.standsApp(t, two, "two")
	serving := func(upstream string) RoutingTable {
		return RoutingTable{
			Grace:  DrainWindow,
			Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: upstream}},
			Claims: []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}},
		}
	}
	held := stood.moves(t, routingTableItem().Content, serving(one))

	var (
		mu      sync.Mutex
		dropped []string
		bodies  = map[string]int{}
		group   sync.WaitGroup
	)
	done := make(chan struct{})
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
			for {
				select {
				case <-done:
					return
				default:
				}
				request, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+proxyPort+"/", nil)
				request.Host = claimed
				said, err := client.Do(request)
				var body []byte
				if err == nil {
					body, err = io.ReadAll(said.Body)
					said.Body.Close()
					if err == nil && said.StatusCode != http.StatusOK {
						err = errors.New(said.Status)
					}
				}
				mu.Lock()
				if err != nil {
					dropped = append(dropped, err.Error())
				} else {
					bodies[string(body)]++
				}
				mu.Unlock()
			}
		}()
	}

	time.Sleep(500 * time.Millisecond)
	flips := 0
	for _, upstream := range []string{two, one, two, one, two, one, two, one, two, one} {
		held = stood.moves(t, held, serving(upstream))
		flips++
		time.Sleep(200 * time.Millisecond)
	}
	close(done)
	group.Wait()

	if len(dropped) > 0 {
		t.Errorf("%d of the requests made while %d flips moved %s between two standing upstreams went unanswered, and a release is meant to drop nothing while one release takes over from the other: %v",
			len(dropped), flips, claimed, dropped[:min(len(dropped), 5)])
	}
	if bodies["one"] == 0 || bodies["two"] == 0 {
		t.Errorf("the requests made across the flips were answered %v, want both upstreams: a flip that never moved the route drops nothing and proves nothing", bodies)
	}
}

func TestARealProxyServesTheNewReleaseWhenTheRetiredOneStopsUnderAHealthProbe(t *testing.T) {
	stood := proxyStanding(t)

	retired, next := "shop-web-1111:"+providerkit.InjectedPortText, "shop-web-2222:"+providerkit.InjectedPortText
	stood.standsSlowApp(t, retired, 2*time.Second)
	stood.standsApp(t, next, "two")
	serving := func(upstream string) RoutingTable {
		return RoutingTable{
			Grace:  DrainWindow,
			Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: upstream, Health: "/up"}},
			Claims: []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}},
		}
	}
	held := stood.moves(t, routingTableItem().Content, serving(retired))
	stood.writes(t, held, serving(next))
	stood.drives(t, "gate", "--deploy-timeout", "10", next+"/up")
	stood.drives(t, "flip", "--drain-timeout", "30", "--retire", retired, ProxyConfigMount)
	name, _, _ := strings.Cut(retired, ":")
	if out, err := exec.Command(dockerEngine, "rm", "--force", name).CombinedOutput(); err != nil {
		t.Fatalf("stop the retired release: %v\n%s", err, out)
	}

	var answered []string
	for range 10 {
		time.Sleep(200 * time.Millisecond)
		request, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+proxyPort+"/", nil)
		request.Host = claimed
		said, err := http.DefaultClient.Do(request)
		if err != nil {
			answered = append(answered, err.Error())
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(said.Body, 1<<12))
		said.Body.Close()
		if said.StatusCode != http.StatusOK || string(body) != "two" {
			answered = append(answered, fmt.Sprintf("%d %q", said.StatusCode, body))
		}
	}
	if len(answered) > 0 {
		t.Errorf("%s was answered %v after the release whose gate passed took over from one stopped once the drain returned: the proxy's probe of the retired release began before the flip, the stop cut it, and the failed probe marked the route down until the next probe",
			claimed, answered)
	}
}

func askedFor(hostname string, timeout time.Duration) (string, error) {
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+proxyPort+"/", nil)
	if err != nil {
		return "", err
	}
	request.Host = hostname
	said, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return "", err
	}
	defer said.Body.Close()
	body, err := io.ReadAll(io.LimitReader(said.Body, 1<<12))
	if err == nil && said.StatusCode != http.StatusOK {
		err = errors.New(said.Status)
	}
	return string(body), err
}

func TestARealProxyCallsAnUpstreamIdleOnlyOnceTheFlipRetiringItHasDrainedIt(t *testing.T) {
	stood := proxyStanding(t)

	retired, next := "shop-web-1111:"+providerkit.InjectedPortText, "shop-web-2222:"+providerkit.InjectedPortText
	stood.standsSlowApp(t, retired, 4*time.Second)
	stood.standsApp(t, next, "two")
	serving := func(upstream string) RoutingTable {
		return RoutingTable{
			Grace:  DrainWindow,
			Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: upstream}},
			Claims: []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}},
		}
	}
	held := stood.moves(t, routingTableItem().Content, serving(retired))
	if idle := strings.TrimSpace(stood.drives(t, "idle", retired)); idle != "" {
		t.Errorf("idle named %q while the route still dials it", idle)
	}

	inFlight := make(chan error, 1)
	go func() {
		_, err := askedFor(claimed, 30*time.Second)
		inFlight <- err
	}()
	time.Sleep(500 * time.Millisecond)
	stood.writes(t, held, serving(next))
	flipped := make(chan string, 1)
	go func() {
		said, err := exec.Command(dockerEngine, "exec", stood.name, ProxyHelperMount,
			"flip", "--drain-timeout", "30", "--retire", retired, ProxyConfigMount).CombinedOutput()
		flipped <- fmt.Sprintf("%v %s", err, said)
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if body, err := askedFor(claimed, 2*time.Second); err == nil && body == "two" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never answered from %s after the flip: %s", claimed, next, <-flipped)
		}
	}

	if idle := strings.TrimSpace(stood.drives(t, "idle", retired, next)); idle != "" {
		t.Errorf("idle named %q while the flip that retired %s was still draining a request from it: a failed release that removes what idle names cuts that request", idle, retired)
	}
	if err := <-inFlight; err != nil {
		t.Errorf("the request in flight when the route moved was answered %v", err)
	}
	t.Logf("the flip said %s", <-flipped)
	if idle := strings.TrimSpace(stood.drives(t, "idle", retired, next)); idle != retired {
		t.Errorf("idle named %q once the flip retiring %s had drained it and returned, want %s alone", idle, retired, retired)
	}
}
