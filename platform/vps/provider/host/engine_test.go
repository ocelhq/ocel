package host

import (
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

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

func seenByTheEngine(t *testing.T) string {
	t.Helper()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("this machine has no home directory to hand the engine a bind source from")
	}
	dir, err := os.MkdirTemp(home, "ocel-probe-")
	if err != nil {
		t.Skip("nothing under this home directory can be written, so the engine can be handed no bind source")
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	at := filepath.Join(dir, "seen")
	if err := os.WriteFile(at, []byte("what the engine must be able to read\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	said, err := exec.Command(dockerEngine, "run", "--rm", "--volume", at+":/seen:ro",
		ProxyImage, "test", "-f", "/seen").CombinedOutput()
	if err != nil {
		t.Skipf("this machine's engine cannot read %s, so a bind source cannot be handed to it here: %s", dir, said)
	}
	return dir
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
	dir := seenByTheEngine(t)
	name, network := probeName(t), probeName(t)+"-net"

	arch, err := Architecture(runtime.GOARCH)
	if err != nil {
		t.Skipf("no flip helper is built for a machine reporting %q", runtime.GOARCH)
	}
	config, helper := filepath.Join(dir, proxyConfigName), filepath.Join(dir, proxyHelperName)
	if err := os.WriteFile(config, proxyBaseline, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, proxyHelper(arch), 0o750); err != nil {
		t.Fatal(err)
	}
	pins := filepath.Join(dir, "pins")
	for _, made := range []string{filepath.Join(dir, "data"), pins} {
		if err := os.MkdirAll(made, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stood := standingProxy{name: name, network: network, dir: dir, pins: pins, helper: helper, here: func(written string) string {
		return strings.NewReplacer(ProxyPins, pins, proxyRoot, dir, ProxyHelper, helper,
			quoted(ProxyContainer), quoted(name), quoted(ProxyNetwork), quoted(network)).Replace(written)
	}}

	if out, err := exec.Command(dockerEngine, "network", "create", network).CombinedOutput(); err != nil {
		t.Fatalf("create the network every deploy resolves across: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command(dockerEngine, "network", "rm", network).Run() })
	t.Cleanup(func() { taken(t, name) })

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
	stated.Content = []byte(strings.Replace(string(proxyFactsOver([]string{
		dir + ":" + proxyConfigDir + ":ro",
		helper + ":" + ProxyHelperMount + ":ro",
		stood.pins + ":" + proxyPinsMount + ":ro",
		filepath.Join(dir, "data") + ":" + proxyDataMount,
	})), "networks="+ProxyNetwork+" ", "networks="+stood.network+" ", 1))
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
		if exited, ok := err.(*exec.ExitError); ok {
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
	stood, err := exec.Command(dockerEngine, "run", "--rm", "--detach", "--name", name,
		"--network", p.network, ProxyImage,
		"caddy", "respond", "--listen", ":"+AppPort, body).CombinedOutput()
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
	rendered, err := RenderProxyConfig(flipped)
	if err != nil {
		t.Fatalf("RenderProxyConfig() = %v", err)
	}
	write := exec.Command("/bin/sh", "-c", stood.here(stagedWrite(contentSum(proxyBaseline))))
	write.Stdin = strings.NewReader(string(rendered))
	if out, err := write.CombinedOutput(); err != nil {
		t.Fatalf("the staged write a deploy makes = %v\n%s", err, out)
	}

	if read := stood.drives(t, "flip", proxyConfigMount); strings.TrimSpace(read) != "" {
		t.Logf("the flip said %q", strings.TrimSpace(read))
	}

	upstream := flipped.Routes[0].Upstream
	if held := stood.drives(t, "config", "apps/http/servers/"+proxyServer+"/routes"); !strings.Contains(held, upstream) {
		t.Fatalf("the running proxy holds\n%s\nafter a deploy moved a config naming %s into place: the deploy writes %s by staging beside it and renaming, and a proxy handed that file through a bind of the file itself keeps reading the inode it was started on and reloads whatever it was seeded with",
			strings.TrimSpace(held), upstream, ProxyConfig)
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
