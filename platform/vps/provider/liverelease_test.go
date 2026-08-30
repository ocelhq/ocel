package vps_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	liveApp     = "web"
	liveOwner   = "ocel--shop--production"
	livePointer = "@production"
	healthPath  = "/healthz"
	drainPort   = "9"
)

type release struct {
	physical string
	address  string
}

func onABoxServingContainers(t *testing.T) (machine, *vps.Provider) {
	t.Helper()
	vm := live(t)
	bootstrapped(t, vm, providerkit.ClassProduction)
	fixtures(t, vm)
	t.Cleanup(func() {
		vm.ssh(t, "sudo docker ps -aq --filter label="+host.LabelApp+" | xargs -r sudo docker rm -f >/dev/null 2>&1 || true")
	})
	p := vm.deploying(t)
	if err := p.Host().ClaimHosts(context.Background(), []host.HostClaim{{Hostname: host.ProxyContainer, Owner: liveOwner, Pointer: edge.DefaultPointer}}); err != nil {
		t.Fatalf("ClaimHosts(%s) = %v", host.ProxyContainer, err)
	}
	return vm, p
}

func livePlan(t *testing.T, tag string) providerkit.StackPlan {
	t.Helper()
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(tag))
	return providerkit.StackPlan{
		Ref:  providerkit.StackRef{Project: "shop", Class: providerkit.ClassProduction, Name: stack},
		Kind: providerkit.StackApp,
		App: &providerkit.AppPlan{
			App:             liveApp,
			Compute:         providerkit.ComputeContainer,
			Deployment:      hex.EncodeToString(sum[:])[:32],
			Image:           fixtureAt(tag),
			HealthCheckPath: healthPath,
		},
	}
}

func standsUp(t *testing.T, p *vps.Provider, tag string) release {
	t.Helper()
	standing, err := p.ProvisionContainers(context.Background(), livePlan(t, tag), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers(%s) = %v", tag, err)
	}
	if len(standing) != 1 {
		t.Fatalf("ProvisionContainers(%s) stood up %v", tag, standing)
	}
	return release{physical: standing[0].Physical, address: standing[0].Physical + ":" + host.AppPort}
}

func releasing(p *vps.Provider, held release, retire string, drain time.Duration, report providerkit.Reporter) error {
	return p.Host().Release(context.Background(), host.Release{
		RouteKey:      host.RouteKey{Owner: liveOwner, Pointer: livePointer, App: liveApp},
		Target:        held.address,
		Retire:        retire,
		HealthPath:    healthPath,
		DeployTimeout: 30 * time.Second,
		DrainTimeout:  drain,
	}, report)
}

func servedBy(t *testing.T, vm machine, path string) string {
	t.Helper()
	return strings.TrimSpace(vm.peers(t, "curl -sS -m 10 http://"+host.ProxyContainer+path))
}

func TestLiveAReleaseServesThroughTheProxyAndNothingElseOnTheBoxCanReachIt(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	one := standsUp(t, p, "one")
	if err := releasing(p, one, "", 30*time.Second, nil); err != nil {
		t.Fatalf("Release() of a first deploy = %v", err)
	}

	if served := servedBy(t, vm, "/"); served != "one" {
		t.Fatalf("the proxy served %q, want the release that was just flipped onto", served)
	}
	if app := vm.inspects(t, "container", one.physical, host.LabelSelector(host.LabelApp)); app != liveApp {
		t.Errorf("the container carries %q under %s, and retention is a set difference over that label", app, host.LabelApp)
	}
	if ref := vm.inspects(t, "container", one.physical, host.LabelSelector(host.LabelRef)); ref != fixtureAt("one") {
		t.Errorf("the container carries %q under %s, want the image ref it runs", ref, host.LabelRef)
	}
	if published := vm.inspects(t, "container", one.physical, "{{json .HostConfig.PortBindings}}"); published != "{}" && published != "null" {
		t.Errorf("the app publishes %s, and the proxy is meant to be the only path to it", published)
	}
	if policy := vm.inspects(t, "container", one.physical, "{{.HostConfig.RestartPolicy.Name}}"); policy != "unless-stopped" {
		t.Errorf("the app is restarted %q, and a reboot would take the box's only workload down", policy)
	}

	if reached := vm.peers(t, "curl -sS -m 5 -o /dev/null -w '%{http_code}' http://"+host.ProxyContainer+"/"); !strings.Contains(reached, "200") {
		t.Fatalf("a peer on the shared network cannot reach the proxy on port 80 at all (%q), so what it cannot reach proves nothing", reached)
	}
	for what, port := range map[string]string{"the admin endpoint": adminPort, "the drain server": drainPort} {
		reached := vm.peers(t, "curl -sS -m 5 -o /dev/null -w '%{http_code}' http://"+host.ProxyContainer+":"+port+"/")
		if strings.Contains(reached, "200") {
			t.Errorf("a peer on the shared network reached %s on %s and got %q", what, port, strings.TrimSpace(reached))
		}
	}
	if bound := vm.ssh(t, "ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null || true"); strings.Contains(bound, ":"+adminPort) {
		t.Errorf("something on this host listens on %s:\n%s", adminPort, bound)
	}
	if mode := strings.TrimSpace(vm.inside(t, "stat -c %a "+quote(host.ProxyAdminSocket))); mode != "600" {
		t.Errorf("%s stands at %q, want 600: the socket's permissions are the whole of its access control", host.ProxyAdminSocket, mode)
	}
}

func TestLiveARedeployUnderContinuousLoadDropsNothingAndDrainsWhenTheHeldRequestReturns(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	one := standsUp(t, p, "one")
	if err := releasing(p, one, "", 30*time.Second, nil); err != nil {
		t.Fatalf("Release() of a first deploy = %v", err)
	}
	two := standsUp(t, p, "two")

	var group sync.WaitGroup
	var hammered, held string
	var samples []string
	var sampling sync.Mutex
	done := make(chan struct{})

	group.Add(3)
	go func() {
		defer group.Done()
		hammered = vm.peers(t, "for i in $(seq 1 200); do curl -sS -m 10 -o /dev/null -w '%{http_code} ' http://"+
			host.ProxyContainer+"/; sleep 0.1; done")
	}()
	go func() {
		defer group.Done()
		held = vm.peers(t, "curl -sS -m 30 -w ' %{http_code}' 'http://"+host.ProxyContainer+"/hold?s=8'")
	}()
	go func() {
		defer group.Done()
		for {
			select {
			case <-done:
				return
			case <-time.After(500 * time.Millisecond):
				read := vm.drives(t, "upstreams")
				sampling.Lock()
				samples = append(samples, read)
				sampling.Unlock()
			}
		}
	}()

	time.Sleep(2 * time.Second)
	began := time.Now()
	if err := releasing(p, two, one.address, 30*time.Second, nil); err != nil {
		close(done)
		group.Wait()
		t.Fatalf("Release() over a box under load = %v", err)
	}
	took := time.Since(began)
	close(done)
	group.Wait()

	if took > 20*time.Second {
		t.Errorf("the release took %s with a request held for 8s and a 30s window, so the drain waited out its ceiling rather than returning on the ack", took)
	}
	if !strings.Contains(held, "one 200") {
		t.Errorf("the request held across the flip answered %q, want the release it started against, with its original status", strings.TrimSpace(held))
	}
	for _, code := range strings.Fields(hammered) {
		if !strings.HasPrefix(code, "2") {
			t.Errorf("a request under continuous load answered %s during the flip:\n%s", code, hammered)
			break
		}
	}
	if served := servedBy(t, vm, "/"); served != "two" {
		t.Errorf("the proxy serves %q after the flip", served)
	}

	both, zeroed := false, false
	sampling.Lock()
	defer sampling.Unlock()
	for _, read := range samples {
		if strings.Contains(read, one.address) && strings.Contains(read, two.address) {
			both = true
		}
		if strings.Contains(read, one.address) && strings.Contains(read, `"num_requests":0`) {
			zeroed = true
		}
	}
	if !both {
		t.Errorf("the retired upstream never appeared beside the new one in %s:\n%v\nthe drain reads the live config's upstreams, so a retired container that leaves the pool at the flip cannot be waited on at all",
			"/reverse_proxy/upstreams", samples)
	}
	if !zeroed {
		t.Errorf("the retired upstream's in-flight count was never read as zero:\n%v", samples)
	}
	if vm.running(t, one.physical) {
		t.Error("the retired container is still running after the release returned")
	}
	if !vm.running(t, two.physical) {
		t.Error("the new container is not running after the release returned")
	}
	if steady := vm.drives(t, "config apps/http/servers"); strings.Contains(steady, "ocel_drain") {
		t.Errorf("the proxy still serves a drain server after the release:\n%s", steady)
	}
}

func TestLiveARequestOutstandingPastTheDrainWindowGetsFiveOhTwo(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	one := standsUp(t, p, "one")
	if err := releasing(p, one, "", 30*time.Second, nil); err != nil {
		t.Fatalf("Release() of a first deploy = %v", err)
	}
	two := standsUp(t, p, "two")

	var group sync.WaitGroup
	var held string
	group.Add(1)
	go func() {
		defer group.Done()
		held = vm.peers(t, "curl -sS -m 60 -o /dev/null -w '%{http_code}' 'http://"+host.ProxyContainer+"/hold?s=25'")
	}()
	time.Sleep(2 * time.Second)

	warned := &said{}
	if err := releasing(p, two, one.address, 3*time.Second, warned); err != nil {
		group.Wait()
		t.Fatalf("Release() whose drain expired = %v, want the new release serving and a warning", err)
	}
	group.Wait()

	if !strings.Contains(held, "502") {
		t.Errorf("a request outstanding past the drain window answered %q, want 502: it dies with its backend rather than being cancelled by the proxy", strings.TrimSpace(held))
	}
	if vm.running(t, one.physical) {
		t.Error("the retired container was not stopped at the ceiling")
	}
	if warned.at("502") < 0 || warned.at(one.address) < 0 {
		t.Errorf("an expired drain warned %v, want the count still in flight and what its clients get", warned.lines)
	}
	if warned.at("websocket") < 0 {
		t.Errorf("the drain-expiry warning reads %v and leaves the fate of a hijacked connection implied", warned.lines)
	}
}

func TestLiveAReleaseThatCannotPassItsGateLeavesThePreviousOneServing(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	one := standsUp(t, p, "one")
	if err := releasing(p, one, "", 30*time.Second, nil); err != nil {
		t.Fatalf("Release() of a first deploy = %v", err)
	}
	routed := vm.drives(t, "config apps/http/servers/ocel/routes")

	refusals := map[string]string{}
	for _, tag := range []string{"sick", "hung", "crasher"} {
		broken := standsUp(t, p, tag)
		err := releasing(p, broken, one.address, 5*time.Second, nil)
		if err == nil {
			t.Fatalf("a release of the %s fixture passed its gate", tag)
		}
		refusals[tag] = err.Error()

		if served := servedBy(t, vm, "/"); served != "one" {
			t.Errorf("after the %s release failed the proxy serves %q, want the previous release still standing", tag, served)
		}
		if now := vm.drives(t, "config apps/http/servers/ocel/routes"); now != routed {
			t.Errorf("the %s release changed the proxy's routes:\n%s\nwant\n%s", tag, now, routed)
		}
		if !vm.running(t, one.physical) {
			t.Fatalf("the %s release stopped the previous container", tag)
		}
		if stood := vm.inspects(t, "container", broken.physical, "{{.Id}}"); stood != "" {
			t.Errorf("the %s release left its container standing as %s", tag, stood)
		}
		if held := strings.TrimSpace(vm.ssh(t, "sudo cat "+host.ProxyConfig)); strings.Contains(held, broken.physical) {
			t.Errorf("the %s release left %s naming an upstream the proxy never accepted, and a restart would adopt it", tag, host.ProxyConfig)
		}
	}

	for _, wanted := range []string{"health.path", healthPath, "404"} {
		if !strings.Contains(refusals["sick"], wanted) {
			t.Errorf("an app answering 404 on its health path is refused with\n%s\nwhich never names %s", refusals["sick"], wanted)
		}
	}
	if !strings.Contains(refusals["hung"], "never answered") {
		t.Errorf("an app that accepts and never answers is refused with\n%s\nand answered-with-N is a different bug from never-answered", refusals["hung"])
	}
	if !strings.Contains(refusals["hung"], "Status=running") || !strings.Contains(refusals["hung"], "RestartCount=0") {
		t.Errorf("a hung app is refused with\n%s\nand never says it is up and never restarted", refusals["hung"])
	}
	if !strings.Contains(refusals["hung"], "(no output)") {
		t.Errorf("a hung app that wrote nothing is refused with\n%s\nrather than with the absence said out loud", refusals["hung"])
	}
	if refusals["hung"] == refusals["crasher"] || refusals["sick"] == refusals["crasher"] {
		t.Error("a crash-looping app is refused in the same words as a hung one, and the restart policy makes the loop invisible without them")
	}
	if !strings.Contains(refusals["crasher"], "Status=restarting") && !strings.Contains(refusals["crasher"], "ExitCode=3") {
		t.Errorf("a crash-looping app is refused with\n%s\nwhich reads as neither restarting nor exited", refusals["crasher"])
	}
}

func TestLiveHijackedConnectionsDoNotSurviveADeploy(t *testing.T) {
	vm, p := onABoxServingContainers(t)

	one := standsUp(t, p, "one")
	if err := releasing(p, one, "", 30*time.Second, nil); err != nil {
		t.Fatalf("Release() of a first deploy = %v", err)
	}
	two := standsUp(t, p, "two")

	var group sync.WaitGroup
	var streamed, upgraded string
	var samples []string
	var sampling sync.Mutex
	done := make(chan struct{})

	group.Add(3)
	go func() {
		defer group.Done()
		streamed = vm.peers(t, "curl -sS -m 40 -o /dev/null -w '%{http_code} %{time_total}' http://"+host.ProxyContainer+"/sse")
	}()
	go func() {
		defer group.Done()
		upgraded = vm.peers(t, "curl -sS -m 40 -o /dev/null -w '%{http_code} %{time_total}' -H 'Connection: Upgrade' -H 'Upgrade: websocket' http://"+
			host.ProxyContainer+"/ws")
	}()
	go func() {
		defer group.Done()
		for {
			select {
			case <-done:
				return
			case <-time.After(500 * time.Millisecond):
				read := vm.drives(t, "upstreams")
				sampling.Lock()
				samples = append(samples, read)
				sampling.Unlock()
			}
		}
	}()
	time.Sleep(3 * time.Second)

	warned := &said{}
	began := time.Now()
	if err := releasing(p, two, one.address, 8*time.Second, warned); err != nil {
		close(done)
		group.Wait()
		t.Fatalf("Release() with hijacked connections open = %v", err)
	}
	took := time.Since(began)
	close(done)
	group.Wait()

	t.Logf("a release with an sse stream and a websocket open against the retired upstream took %s", took)
	t.Logf("the retired upstream reported %v", samples)
	t.Logf("the sse client saw %q and the upgraded client saw %q", strings.TrimSpace(streamed), strings.TrimSpace(upgraded))

	if !strings.HasPrefix(strings.TrimSpace(upgraded), "101") {
		t.Fatalf("the upgraded client saw %q, want the 101 this measurement is about", strings.TrimSpace(upgraded))
	}
	if held := seconds(t, upgraded); held > 6 {
		t.Errorf("the hijacked connection lived %.1fs across a flip made 3s in, so caddy no longer cuts it at cutover and the deploy's own statement about websockets is wrong", held)
	}
	if took < 7*time.Second {
		t.Errorf("the release returned in %s inside an 8s window with an sse stream still open, so the stream no longer holds the retired upstream and the statement that an sse app drains at its ceiling is wrong", took)
	}
	if warned.at("still held") < 0 {
		t.Errorf("a release with an sse stream open warned %v, want the expiry the measurement says it causes", warned.lines)
	}
	if warned.at("websocket") < 0 || warned.at("502") < 0 {
		t.Errorf("the warning reads %v and leaves the hijacked fate or the ceiling's outcome implied", warned.lines)
	}
	if vm.running(t, one.physical) {
		t.Error("the retired container survived a deploy that held its connections open past the window")
	}
	if served := servedBy(t, vm, "/"); served != "two" {
		t.Errorf("the proxy serves %q after a deploy whose drain expired, and the new release is serving either way", served)
	}
}

func seconds(t *testing.T, written string) float64 {
	t.Helper()
	fields := strings.Fields(written)
	if len(fields) != 2 {
		t.Fatalf("a peer reported %q, want a status and a duration", written)
	}
	held, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		t.Fatalf("a peer reported %q as a duration: %v", fields[1], err)
	}
	return held
}
