package host

import (
	"bytes"
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
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
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
		{"container", caddy.Container, "docker rm --force " + caddy.Container},
		{"container", SwitchboardContainer, "docker rm --force " + SwitchboardContainer},
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
	board   string
	network string
	dir     string
	pins    string
	table   string
	binary  string
	here    func(string) string
}

func proxyStanding(t *testing.T) standingProxy {
	t.Helper()

	engineOrSkip(t)
	name, network := probeName(t), enginetest.Network(t)
	board := name + "-board"
	t.Cleanup(func() { taken(t, name) })
	t.Cleanup(func() { taken(t, board) })
	dir := enginetest.BindSource(t)

	arch, err := Architecture(runtime.GOARCH)
	if err != nil {
		t.Skipf("no switchboard is built for a machine reporting %q", runtime.GOARCH)
	}
	stood := standingProxy{name: name, board: board, network: network, dir: dir}
	proxied, routing, switching := filepath.Join(dir, "proxy"), filepath.Join(dir, "routing"), filepath.Join(dir, "switchboard")
	stood.pins, stood.table = filepath.Join(dir, "pins"), filepath.Join(routing, filepath.Base(live.RoutingTable))
	stood.binary = filepath.Join(switching, switchboard.Name)
	for _, made := range []string{proxied, filepath.Join(proxied, "data"), routing, switching, stood.pins, filepath.Join(dir, "control"), filepath.Join(dir, "connector")} {
		if err := os.MkdirAll(made, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, seeded := range map[string][]byte{filepath.Join(proxied, caddy.ConfigName): proxyConfigItem().Content, stood.table: routingTableItem().Content} {
		if err := os.WriteFile(path, seeded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runnable(t, stood.binary, switchboardBinary(arch), 0o755)
	unprivileged := quoted("--security-opt") + " " + quoted(noNewPrivileges) + " "
	if pinnable() {
		unprivileged = ""
	} else {
		t.Log("this engine refuses to exec anything under no-new-privileges, so the switchboard stands here without it")
	}
	stood.here = strings.NewReplacer(
		SwitchboardAddress, board+":"+switchboardPort,
		switchboard.ControlDir, filepath.Join(dir, "control"),
		SwitchboardDir, switching,
		ConnectorRun, filepath.Join(dir, "connector"),
		caddy.PinsDir, stood.pins,
		proxyRoot, proxied,
		live.RoutingDir, routing,
		routingLock, dir,
		quoted(caddy.Container), quoted(name),
		quoted(SwitchboardContainer), quoted(board),
		quoted(ProxyNetwork), quoted(network),
		`"`+ProxyNetwork+`"`, `"`+network+`"`,
		unprivileged, "",
	).Replace

	taken(t, board)
	taken(t, name)
	for what, script := range map[string]string{
		"the switchboard": switchboardStanding(switchboardBinary(arch)).writing(containerRising),
		"the proxy":       proxyWriting(containerRising),
	} {
		if out, err := exec.Command("/bin/sh", "-c", stood.here(script)).CombinedOutput(); err != nil {
			t.Fatalf("the write that stands %s up = %v\n%s", what, err, out)
		}
	}
	return stood
}

var pinnable = sync.OnceValue(func() bool {
	said, err := exec.Command(dockerEngine, "run", "--rm", "--security-opt", noNewPrivileges, "--entrypoint", "/bin/true", caddy.Image).CombinedOutput()
	return err == nil || !strings.Contains(string(said), "operation not permitted")
})

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
	arch, _ := Architecture(runtime.GOARCH)

	for _, standing := range []struct {
		name  string
		as    boxContainer
		binds []string
	}{
		{stood.name, frontProxy(), []string{
			filepath.Join(stood.dir, "proxy") + ":" + caddy.ConfigDir + ":ro",
			stood.pins + ":" + caddy.PinsMount + ":ro",
			filepath.Join(stood.dir, "proxy", "data") + ":" + caddy.DataMount,
		}},
		{stood.board, switchboardStanding(switchboardBinary(arch)), []string{
			filepath.Join(stood.dir, "switchboard") + ":" + switchboardMount + ":ro",
			filepath.Join(stood.dir, "routing") + ":" + filepath.Join(stood.dir, "routing") + ":ro",
			filepath.Join(stood.dir, "connector") + ":" + filepath.Join(stood.dir, "connector") + ":ro",
			filepath.Join(stood.dir, "control") + ":" + filepath.Join(stood.dir, "control"),
		}},
	} {
		rendered, err := exec.Command("/bin/sh", "-c", stood.here(standing.as.probe())).Output()
		if err != nil {
			t.Fatalf("probe %s on this machine: %v", standing.name, err)
		}
		observed, _, err := readSurvey(string(rendered))
		if err != nil {
			t.Fatal(err)
		}
		stated := standing.as.item("")
		stated.Name = standing.name
		stated.Content = []byte(stood.here(string(standing.as.factsOver(standing.binds))))
		if observed[stated.ID()] != stated.Digest() {
			box, _ := exec.Command(dockerEngine, "inspect", "--type", "container", "--format", ContainerFactTemplate, standing.name).Output()
			t.Errorf("a real engine reports %s as something other than the item ocel writes it from, so every re-run plans an update over a container that stands:\n%s",
				standing.name, compared(canonical(stood.here(string(box))), strings.TrimSpace(string(stated.Content))))
		}
	}
}

func TestAProbeWithNoRootReadsABindWhoseSourceTheHostReplacedAsMoved(t *testing.T) {
	stood := proxyStanding(t)
	arch, _ := Architecture(runtime.GOARCH)

	for _, standing := range []struct {
		name     string
		as       boxContainer
		replaced string
		binds    []string
	}{
		{stood.name, frontProxy(), stood.pins, []string{
			filepath.Join(stood.dir, "proxy") + ":" + caddy.ConfigDir + ":ro",
			stood.pins + ":" + caddy.PinsMount + ":ro",
			filepath.Join(stood.dir, "proxy", "data") + ":" + caddy.DataMount,
		}},
		{stood.board, switchboardStanding(switchboardBinary(arch)), filepath.Join(stood.dir, "connector"), []string{
			filepath.Join(stood.dir, "switchboard") + ":" + switchboardMount + ":ro",
			filepath.Join(stood.dir, "routing") + ":" + filepath.Join(stood.dir, "routing") + ":ro",
			filepath.Join(stood.dir, "connector") + ":" + filepath.Join(stood.dir, "connector") + ":ro",
			filepath.Join(stood.dir, "control") + ":" + filepath.Join(stood.dir, "control"),
		}},
	} {
		if err := os.Rename(standing.replaced, standing.replaced+".gone"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(standing.replaced, 0o755); err != nil {
			t.Fatal(err)
		}
		rendered, err := exec.Command("/bin/sh", "-c", stood.here(standing.as.probe())).Output()
		if err != nil {
			t.Fatalf("probe %s on this machine: %v", standing.name, err)
		}
		observed, _, err := readSurvey(string(rendered))
		if err != nil {
			t.Fatal(err)
		}
		moved := standing.as.item("")
		moved.Name = standing.name
		moved.Content = bytes.Replace([]byte(stood.here(string(standing.as.factsOver(standing.binds)))),
			[]byte(mountsFact+mountsHeld), []byte(mountsFact+mountsMoved), 1)
		if observed[moved.ID()] != moved.Digest() {
			t.Errorf("%s reads %s, a directory the host replaced after it started, and a probe with no root reports it as something other than a moved mount: an unelevated describe then calls a container current that serves a directory which is gone",
				standing.name, standing.replaced)
		}
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

	run := exec.Command(dockerEngine, append([]string{"exec", p.board, SwitchboardMounted}, argv...)...)
	said, err := run.Output()
	if err != nil {
		var stderr string
		exited := &exec.ExitError{}
		if errors.As(err, &exited) {
			stderr = string(exited.Stderr)
		}
		t.Fatalf("%v against the running switchboard = %v\n%s", argv, err, stderr)
	}
	return string(said)
}

func (p standingProxy) reloads(t *testing.T) {
	t.Helper()

	if out, err := exec.Command(dockerEngine, "exec", p.name, "caddy", "reload", "--config", caddy.ConfigMount, "--address", "unix/"+caddy.AdminSocket).CombinedOutput(); err != nil {
		t.Fatalf("reload the running proxy onto its config = %v\n%s", err, out)
	}
}

func (p standingProxy) standsApp(t *testing.T, upstream, body string) {
	t.Helper()

	name, _, _ := strings.Cut(upstream, ":")
	exec.Command(dockerEngine, "rm", "--force", name).Run()
	run := append([]string{"run", "--rm", "--detach", "--name", name}, enginetest.Labelled(t)...)
	stood, err := exec.Command(dockerEngine, append(run, "--network", p.network, caddy.Image,
		"caddy", "respond", "--listen", ":"+providerkit.InjectedPortText, body)...).CombinedOutput()
	if err != nil {
		t.Skipf("this machine's engine will not run the app the proxy forwards to: %s", stood)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "rm", "--force", name).Run() })
}

func TestATableAndAConfigMovedIntoPlaceAreWhatTheRunningBoxServes(t *testing.T) {
	stood := proxyStanding(t)

	flipped := routed()
	stood.standsApp(t, flipped.Routes[0].Upstream, "the app answered")
	flipped.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood.writes(t, routingTableItem().Content, flipped)
	stood.drives(t, "load", stood.table)
	stood.reloads(t)

	if held, err := exec.Command(dockerEngine, "exec", stood.name, "cat", caddy.ConfigMount).Output(); err != nil || !strings.Contains(string(held), claimed) {
		t.Fatalf("the running proxy reads %s as\n%s\n(%v) after a deploy moved a config naming %s into place: the deploy writes it by staging beside it and renaming, and a proxy handed that file through a bind of the file itself keeps reading the inode it was started on",
			caddy.ConfigMount, held, err, claimed)
	}

	ask := func(hostname string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+caddy.HTTPPort+"/", nil)
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

	if said := ask("unclaimed.example.com"); said.StatusCode != http.StatusNotFound || said.Header.Get(edge.HeaderEdge) != switchboard.EdgeName {
		t.Errorf("a hostname nothing on this box claims was answered %d carrying %s: %q, want a 404 naming this edge",
			said.StatusCode, edge.HeaderEdge, said.Header.Get(edge.HeaderEdge))
	}
	said := ask(claimed)
	read, err := io.ReadAll(io.LimitReader(said.Body, 1<<12))
	if err != nil {
		t.Fatalf("read what the proxy answered for %q: %v", claimed, err)
	}
	if said.StatusCode != http.StatusOK || string(read) != "the app answered" {
		t.Errorf("the hostname %q claims was answered %d %q, want the body of the app standing on %s: the switchboard is handed the directory the table is renamed into, and one handed the file keeps routing the seed",
			surface, said.StatusCode, read, flipped.Routes[0].Upstream)
	}
	if said.Header.Get(edge.HeaderEdge) != switchboard.EdgeName {
		t.Errorf("the surface's own route answered %s: %q, want %q", edge.HeaderEdge, said.Header.Get(edge.HeaderEdge), switchboard.EdgeName)
	}
}

func TestTheSwitchboardTrustsWhatTheFrontProxyForwardsAndNothingAClientSays(t *testing.T) {
	stood := proxyStanding(t)

	state := routed()
	stood.standsApp(t, state.Routes[0].Upstream, "{http.request.header.X-Forwarded-For}|{http.request.header.X-Forwarded-Proto}|{http.request.host}")
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood.moves(t, routingTableItem().Content, state)

	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+caddy.HTTPPort+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = claimed
	request.Header.Set("X-Forwarded-For", "6.6.6.6")
	request.Header.Set("X-Forwarded-Proto", "https")
	said, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("ask the box for %s: %v", claimed, err)
	}
	defer said.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(said.Body, 1<<12))
	forwarded, rest, _ := strings.Cut(string(body), "|")
	proto, hostname, _ := strings.Cut(rest, "|")
	hops := strings.Split(forwarded, ", ")
	if len(hops) != 2 || slices.Contains(hops, "6.6.6.6") {
		t.Errorf("the app heard X-Forwarded-For %q, want the client the front proxy saw and the front proxy itself: a switchboard that does not trust the front proxy by name drops the first, and a front proxy that trusts its client passes on the spoof", forwarded)
	}
	if proto != "http" {
		t.Errorf("the app heard X-Forwarded-Proto %q over a plain http request that claimed https, want http", proto)
	}
	if hostname != claimed {
		t.Errorf("the app was asked for host %q, want %s passed through both hops", hostname, claimed)
	}
}

func TestTheFrontProxyNamesTheEdgeOnItsOwnAnswerWhileTheSwitchboardIsDown(t *testing.T) {
	stood := proxyStanding(t)

	state := routed()
	stood.standsApp(t, state.Routes[0].Upstream, "the app answered")
	state.Claims = []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}
	stood.moves(t, routingTableItem().Content, state)
	ask := func() *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+caddy.HTTPPort+"/", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = claimed
		said, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("ask the box for %s: %v", claimed, err)
		}
		said.Body.Close()
		return said
	}

	if said := ask(); said.StatusCode != http.StatusOK || !slices.Equal(said.Header.Values(edge.HeaderEdge), []string{switchboard.EdgeName}) {
		t.Errorf("%s was answered %d naming the edge %q, want 200 naming it once as %s", claimed, said.StatusCode, said.Header.Values(edge.HeaderEdge), switchboard.EdgeName)
	}
	if out, err := exec.Command(dockerEngine, "stop", "--time", "1", stood.board).CombinedOutput(); err != nil {
		t.Fatalf("stop the switchboard: %v\n%s", err, out)
	}
	if said := ask(); said.StatusCode != http.StatusBadGateway || !slices.Equal(said.Header.Values(edge.HeaderEdge), []string{switchboard.EdgeName}) {
		t.Errorf("with the switchboard down %s was answered %d naming the edge %q, want caddy's own 502 naming it as %s: a bootstrap recreates the switchboard under a running caddy, and the bind's probe reads the edge off every answer the box gives",
			claimed, said.StatusCode, said.Header.Values(edge.HeaderEdge), switchboard.EdgeName)
	}
}

func (p standingProxy) stages(t *testing.T, held []byte, state RoutingTable, config []byte) []byte {
	t.Helper()

	written := mustWrite(t, state)
	write := exec.Command("/bin/sh", "-c", p.here(stagedWrite(tableDigest(contentSum(held)))))
	write.Stdin = strings.NewReader(pairFed(routingPair{table: written, config: []byte(p.here(string(config)))}))
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
	p.drives(t, "flip", p.table)
	p.reloads(t)
	return written
}

func (p standingProxy) standsSlowApp(t *testing.T, upstream string, slow time.Duration) {
	t.Helper()

	name, _, _ := strings.Cut(upstream, ":")
	exec.Command(dockerEngine, "rm", "--force", name).Run()
	answer := fmt.Sprintf(`while read -r line && [ "$line" != "$(printf '\r')" ]; do :; done; sleep %d; printf 'HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n'`,
		int(slow.Seconds()))
	stood, err := exec.Command(dockerEngine, "run", "--rm", "--detach", "--name", name,
		"--network", p.network, "--entrypoint", "nc", caddy.Image,
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
				request, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+caddy.HTTPPort+"/", nil)
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

func TestARealBoxServesTheNewReleaseTheMomentTheRetiredOneIsRemoved(t *testing.T) {
	stood := proxyStanding(t)

	retired, next := "shop-web-1111:"+providerkit.InjectedPortText, "shop-web-2222:"+providerkit.InjectedPortText
	stood.standsSlowApp(t, retired, 2*time.Second)
	stood.standsApp(t, next, "two")
	serving := func(upstream string) RoutingTable {
		return RoutingTable{
			Grace:  DrainWindow,
			Routes: []AppRoute{{RouteKey: keyed("web"), Upstream: upstream}},
			Claims: []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}},
		}
	}
	held := stood.moves(t, routingTableItem().Content, serving(retired))
	stood.writes(t, held, serving(next))
	stood.drives(t, "gate", "--deploy-timeout", "10", next+"/up")
	stood.drives(t, "flip", "--drain-timeout", "30", "--retire", retired, stood.table)
	name, _, _ := strings.Cut(retired, ":")
	if out, err := exec.Command(dockerEngine, "rm", "--force", name).CombinedOutput(); err != nil {
		t.Fatalf("stop the retired release: %v\n%s", err, out)
	}

	var answered []string
	for range 10 {
		time.Sleep(200 * time.Millisecond)
		request, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+caddy.HTTPPort+"/", nil)
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
		t.Errorf("%s was answered %v after the release whose gate passed took over from one removed once the drain returned: a route that stays down after its retiree goes serves nothing to the deploy that recovers a crashed app",
			claimed, answered)
	}
}

func askedFor(hostname string, timeout time.Duration) (string, error) {
	request, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+caddy.HTTPPort+"/", nil)
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
		said, err := exec.Command(dockerEngine, "exec", stood.board, SwitchboardMounted,
			"flip", "--drain-timeout", "30", "--retire", retired, stood.table).CombinedOutput()
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
