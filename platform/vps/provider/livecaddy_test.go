package vps_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type caddyFront struct {
	front     string
	directory string
	network   string
	theirs    string
}

const (
	caddyServeWait    = 90 * time.Second
	caddyReloadGrant  = "/etc/sudoers.d/ocel-caddy-reload"
	caddyAdminServers = "http://127.0.0.1:2019/config/apps/http/servers"
)

func TestLiveOcelServesBehindTheCaddyPackageAndWritesOnlyOcelCaddy(t *testing.T) {
	vm := liveMachine(t)
	servesBehindCaddy(t, vm, caddyFront{front: "caddy", directory: "/etc/caddy/ocel.d", theirs: "mine.localhost"})
	if left := vm.ssh(t, "sudo test -e "+quote(caddyReloadGrant)+" && echo present || echo gone"); strings.TrimSpace(left) != "gone" {
		t.Errorf("%s outlived the destroy", caddyReloadGrant)
	}
}

func TestLiveOcelServesBehindACaddyContainerOnItsOwnNetwork(t *testing.T) {
	vm := liveMachine(t)
	servesBehindCaddy(t, vm, caddyFront{
		front:     "caddy-container",
		directory: "/etc/ocel-front-caddy/ocel.d",
		network:   "ocel-front-caddy",
		theirs:    "mine.localhost",
	})
}

func (vm machine) servedThroughCaddy(t *testing.T, hostname string) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t, "curl -sk -m 10 --resolve "+quote(hostname+":443:127.0.0.1")+" "+quote("https://"+hostname+"/")+" || true"))
}

func (vm machine) awaitsCaddy(t *testing.T, hostname, want string) {
	t.Helper()
	deadline := time.Now().Add(caddyServeWait)
	for served := vm.servedThroughCaddy(t, hostname); served != want; served = vm.servedThroughCaddy(t, hostname) {
		if time.Now().After(deadline) {
			t.Fatalf("%s answered %q through your Caddy within %s, want %q", hostname, served, caddyServeWait, want)
		}
		time.Sleep(time.Second)
	}
}

func (vm machine) placedCaddy(t *testing.T, directory string) string {
	t.Helper()
	return vm.ssh(t, "sudo cat "+quote(directory+"/"+caddyfile.FileName)+" 2>/dev/null || true")
}

func bindsBehindCaddy(t *testing.T, d *vps.Provider, hostname string) string {
	t.Helper()
	added, err := overTheContract(t, d).AddHostname(context.Background(), &contractv1.HostnameRequest{
		Slug:       frontedSlug,
		Configured: configuredHosts(hostname),
		Edge:       &contractv1.EdgeSelection{},
	})
	if err != nil {
		t.Fatalf("AddHostname() = %v", err)
	}
	_, _, result := drained(t, added)
	if result == nil {
		t.Fatalf("AddHostname(%s) ended with no result", hostname)
	}
	return result.GetError()
}

func unbindsBehindCaddy(t *testing.T, d *vps.Provider, hostname string) {
	t.Helper()
	gone, err := overTheContract(t, d).RemoveHostname(context.Background(), &contractv1.HostnameRequest{
		Slug: frontedSlug,
		Host: hostname,
		Edge: &contractv1.EdgeSelection{},
	})
	if err != nil {
		t.Fatalf("RemoveHostname() = %v", err)
	}
	if _, _, result := drained(t, gone); result == nil || !result.GetSuccess() {
		t.Fatalf("RemoveHostname(%s) = %v, want the hostname given back", hostname, result.GetError())
	}
}

func bootstrapsBehindCaddy(t *testing.T, vm machine, cf caddyFront) (*vps.Proxy, provider.Bootstrap) {
	t.Helper()
	proxy := frontProxy(t, cf.front)
	p := vm.provider(t, func(o *vps.Options) { o.Proxy = proxy })
	t.Cleanup(func() { closing(t, p) })
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if err := bootstrap.Apply(context.Background(), provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
			t.Fatalf("Apply(%s) behind %s = %v", tier, cf.front, err)
		}
	}
	if state := vm.state(t, caddy.Container); state != "gone" {
		t.Errorf("%s is %s on a box your Caddy fronts, want it never started", caddy.Container, state)
	}
	if cf.network != "" {
		if networks := frontNetworks(t, vm, host.SwitchboardContainer); !slices.Contains(networks, cf.network) {
			t.Errorf("%s sits on %q, want it on %s, where your Caddy reaches it by name", host.SwitchboardContainer, networks, cf.network)
		}
	} else if published := vm.inspects(t, "container", host.SwitchboardContainer, "{{json .HostConfig.PortBindings}}"); !strings.Contains(published, `"HostIp":"127.0.0.1","HostPort":"8480"`) {
		t.Errorf("the switchboard publishes %s, want 127.0.0.1:8480 for a Caddy on the host to reach", published)
	}
	return proxy, bootstrap
}

func deploysOneBehindCaddy(t *testing.T, vm machine, cf caddyFront, proxy *vps.Proxy) (*vps.Provider, edge.Edge, edge.EdgeStack) {
	t.Helper()
	ctx := context.Background()
	fixtures(t, vm)
	d := vm.deployingBehind(t, proxy)
	if err := d.PreflightDeploy(ctx, provider.DeployPreflight{Deploy: provider.DeploySpec{
		Slug: frontedSlug, Tier: environment.TierProduction, Apps: []provider.AppEntry{{App: liveApp, Image: fixtureAt("one")}},
	}}); err != nil {
		t.Fatalf("PreflightDeploy() behind %s = %v", cf.front, err)
	}
	opened, err := d.Edges().Open(edge.None)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := opened.Reconcile(ctx, edge.StackSpec{Version: "test", Tier: environment.TierProduction, Slug: frontedSlug}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	promotes(t, d, stack, "p-one", "one", provisioned(t, d, "one"), 1)
	recorded(t, d, frontedSlug, stack.State())
	return d, opened, stack
}

func (vm machine) refusesTheirs(t *testing.T, d *vps.Provider, cf caddyFront) {
	t.Helper()
	if refused := bindsBehindCaddy(t, d, cf.theirs); !strings.Contains(refused, "already served by your Caddy") {
		t.Errorf("binding %s, which a site of yours serves, = %q, want it refused naming where your Caddy serves it", cf.theirs, refused)
	}
	if placed := vm.placedCaddy(t, cf.directory); strings.Contains(placed, cf.theirs) {
		t.Errorf("%s names %s after its bind was refused:\n%s", caddyfile.FileName, cf.theirs, placed)
	}
}

func (vm machine) destroysBehindCaddy(t *testing.T, bootstrap provider.Bootstrap, stack edge.EdgeStack, cf caddyFront) {
	t.Helper()
	ctx := context.Background()
	if err := stack.Destroy(ctx); err != nil {
		t.Errorf("Destroy() = %v", err)
	}
	vm.ssh(t, "sudo docker ps -aq --filter label="+host.LabelApp+" | xargs -r sudo docker rm -f >/dev/null 2>&1 || true")
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		if err := bootstrap.Remove(ctx, tier, nil); err != nil {
			t.Fatalf("Remove(%s) behind %s = %v", tier, cf.front, err)
		}
	}
	if state := vm.state(t, host.SwitchboardContainer); state != "gone" {
		t.Errorf("%s is %s after the box was destroyed", host.SwitchboardContainer, state)
	}
	placed := cf.directory + "/" + caddyfile.FileName
	if left := vm.ssh(t, "sudo test -e "+quote(placed)+" && echo present || echo gone"); strings.TrimSpace(left) != "gone" {
		t.Errorf("%s outlived the destroy", placed)
	}
	vm.frontUntouched(t, cf.front)
}

func servesBehindCaddy(t *testing.T, vm machine, cf caddyFront) {
	t.Helper()
	vm.purges(t)
	t.Cleanup(func() { vm.purges(t) })
	vm.fronted(t, cf.front)
	ctx := context.Background()

	proxy, bootstrap := bootstrapsBehindCaddy(t, vm, cf)
	granted := strings.TrimSpace(vm.ssh(t, "sudo test -e "+quote(caddyReloadGrant)+" && echo present || echo gone"))
	if want := map[bool]string{true: "gone", false: "present"}[proxy.Caddy.Container != ""]; granted != want {
		t.Errorf("%s is %s behind %s, want %s: only a Caddy that runs as caddy.service is reloaded through sudo", caddyReloadGrant, granted, cf.front, want)
	}

	d, opened, stack := deploysOneBehindCaddy(t, vm, cf, proxy)
	vm.refusesTheirs(t, d, cf)

	if refused := bindsBehindCaddy(t, d, frontedHostname); refused != "" {
		t.Fatalf("AddHostname(%s) behind %s = %s", frontedHostname, cf.front, refused)
	}
	if placed := vm.placedCaddy(t, cf.directory); !strings.Contains(placed, frontedHostname) {
		t.Errorf("%s reads\n%s\nafter the bind, want it to name %s", caddyfile.FileName, placed, frontedHostname)
	}
	vm.awaitsCaddy(t, frontedHostname, "one")
	vm.awaitsCaddy(t, cf.theirs, "mine")
	if kind, err := d.ServingRouter(ctx, frontedHostname); err != nil || kind != switchboard.RouterKind {
		t.Errorf("Serving(%s) = %q, %v, want the switchboard proven through %s", frontedHostname, kind, err, cf.front)
	}

	two := provisioned(t, d, "two")
	vm.loads(t, frontedHostname, cf.theirs)
	vm.awaitAnswers(t, "of one before the flip", counting("one", 0))
	vm.awaitAnswers(t, "of your own site before the flip", counting("mine", 0))
	flipping := vm.clock(t)
	promotes(t, d, stack, "p-two", "two", two, 2)
	flipped := vm.clock(t)
	vm.awaitAnswers(t, "of two after the flip", counting("two", flipped))
	heard := vm.stopsLoad(t)
	during := map[string]int{}
	for _, answer := range heard {
		if !strings.HasPrefix(answer.code, "2") {
			t.Errorf("a request for %s through %s asked at %.3f answered %s while the release flipped between %.3f and %.3f", answer.host, cf.front, answer.at, answer.code, flipping, flipped)
		}
		if answer.at >= flipping && answer.at <= flipped {
			during[answer.host]++
		}
		if answer.host == cf.theirs && answer.body != "mine" {
			t.Errorf("your own site %s asked at %.3f answered %q through %s, want mine: it keeps serving throughout the redeploy", cf.theirs, answer.at, answer.body, cf.front)
		}
		if answer.host == frontedHostname && answer.at > flipped && answer.body != "two" {
			t.Errorf("a request through %s asked at %.3f, after the flip finished at %.3f, answered %q, want two", cf.front, answer.at, flipped, answer.body)
		}
	}
	for _, hostname := range []string{frontedHostname, cf.theirs} {
		if during[hostname] == 0 {
			t.Errorf("no request for %s through %s was asked while the release flipped between %.3f and %.3f, so the %d answered prove nothing about the flip", hostname, cf.front, flipping, flipped, len(heard))
		}
	}

	servesAPreviewBehindCaddy(t, vm, d, opened, cf)

	checks, err := d.CheckHost(ctx, provider.HostCheckRequest{Tier: environment.TierProduction})
	if err != nil {
		t.Fatalf("CheckHost() = %v", err)
	}
	for _, check := range checks {
		if check.Verdict == provider.HostFail {
			t.Errorf("%s fails behind %s: %s", check.Subject, cf.front, check.Finding)
		}
	}

	unbindsBehindCaddy(t, d, frontedHostname)
	if placed := vm.placedCaddy(t, cf.directory); strings.Contains(placed, frontedHostname) {
		t.Errorf("%s still names %s after the unbind:\n%s", caddyfile.FileName, frontedHostname, placed)
	}
	if served := vm.servedThroughCaddy(t, frontedHostname); served == "two" {
		t.Errorf("%s still answers %q through your Caddy after the unbind", frontedHostname, served)
	}
	if served := vm.servedThroughCaddy(t, cf.theirs); served != "mine" {
		t.Errorf("%s answered %q after the unbind, want your own site still serving", cf.theirs, served)
	}

	vm.destroysBehindCaddy(t, bootstrap, stack, cf)
}

func servesAPreviewBehindCaddy(t *testing.T, vm machine, d *vps.Provider, opened edge.Edge, cf caddyFront) {
	t.Helper()
	ctx := context.Background()
	if _, err := opened.ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{
		BaseDomain: frontedPreview,
	}); err != nil {
		t.Fatalf("ReconcilePreviewWildcard(%s) behind %s = %v", frontedPreview, cf.front, err)
	}
	previews, err := opened.Reconcile(ctx, edge.StackSpec{Version: "test", Tier: environment.TierPreview, Slug: frontedSlug},
		edge.StackState{GlobalPreview: frontedPreview})
	if err != nil {
		t.Fatalf("Reconcile(preview): %v", err)
	}
	promotesPreview(t, d, previews, frontedSlug, liveApp, fixtureAt("one"), frontedPointer, 1)

	hostname := signLivePreviewHost(frontedSlug, frontedPreview, frontedPointer, liveApp).Hostname
	vm.awaitsCaddy(t, hostname, "one")
	probe := edge.ProbeHostname(edge.PreviewWildcard(frontedPreview))
	if placed := vm.placedCaddy(t, cf.directory); !strings.Contains(placed, probe) || !strings.Contains(placed, hostname) {
		t.Errorf("%s reads\n%s\nwant it naming %s and %s: your Caddy gets a certificate for each preview hostname as it is claimed", caddyfile.FileName, placed, probe, hostname)
	}

	if err := previews.Destroy(ctx); err != nil {
		t.Errorf("Destroy(preview) = %v", err)
	}
	if err := opened.DestroyPreviewWildcard(ctx, frontedPreview); err != nil {
		t.Errorf("DestroyPreviewWildcard(%s) = %v", frontedPreview, err)
	}
}

func coolifysCaddyBox(t *testing.T) machine {
	t.Helper()
	if os.Getenv("OCEL_INCUS_ADDR") == "" {
		t.Skip(unreachable)
	}
	vm := liveMachine(t)
	image := strings.TrimSpace(vm.ssh(t, "sudo docker inspect --type container --format '{{.Config.Image}}' coolify-proxy 2>/dev/null || true"))
	if !strings.Contains(image, "caddy-docker-proxy") {
		t.Skip("this box runs no Coolify switched to Caddy; hand this test a clone of the #1305 Coolify box with its proxy switched to Caddy, as `incus copy p1305-coolify/<snapshot> <name>` and `scripts/incus.sh info <name>`")
	}
	return vm
}

type caddyServer struct {
	Routes []struct {
		Match []struct {
			Host []string `json:"host"`
		} `json:"match"`
	} `json:"routes"`
}

func (vm machine) coolifysOwnHostname(t *testing.T) string {
	t.Helper()
	said := vm.ssh(t, "sudo docker exec coolify-proxy wget -qO- "+quote(caddyAdminServers))
	var servers map[string]caddyServer
	if err := json.Unmarshal([]byte(said), &servers); err != nil {
		t.Fatalf("coolify-proxy's admin endpoint answered %q: %v", said, err)
	}
	for _, server := range servers {
		for _, route := range server.Routes {
			for _, match := range route.Match {
				for _, named := range match.Host {
					if !strings.Contains(named, "*") && !strings.HasPrefix(named, edge.LivenessProbeLabel+".") {
						return named
					}
				}
			}
		}
	}
	t.Fatalf("coolify-proxy serves no hostname of Coolify's:\n%s", said)
	return ""
}

func TestHostToolOcelRefusesWhatWouldBreakCoolifysCaddy(t *testing.T) {
	vm := coolifysCaddyBox(t)
	vm.purges(t)
	t.Cleanup(func() { vm.purges(t) })
	cf := caddyFront{front: "coolify-caddy", directory: "/data/coolify/proxy/caddy/dynamic", network: "coolify"}
	vm.fronted(t, cf.front)
	cf.theirs = vm.coolifysOwnHostname(t)
	ctx := context.Background()

	proxy, bootstrap := bootstrapsBehindCaddy(t, vm, cf)
	d, _, stack := deploysOneBehindCaddy(t, vm, cf, proxy)
	vm.refusesTheirs(t, d, cf)

	before := vm.placedCaddy(t, cf.directory)
	invalid := []byte(frontedHostname + " {\n\tnot_a_directive\n}\n")
	if err := d.Host().FrontProxy().Validate(ctx, invalid); err == nil || !strings.Contains(err.Error(), "not_a_directive") {
		t.Errorf("Validate() of a snippet Coolify's Caddy cannot read = %v, want it refused with what caddy adapt said", err)
	}
	time.Sleep(6 * time.Second)
	if after := vm.placedCaddy(t, cf.directory); after != before {
		t.Errorf("%s reads\n%s\nafter an invalid rendering was refused, want it untouched:\n%s", caddyfile.FileName, after, before)
	}
	if served := vm.ssh(t, "sudo docker exec coolify-proxy wget -qO- "+quote(caddyAdminServers)); !strings.Contains(served, cf.theirs) {
		t.Errorf("coolify-proxy no longer serves %s after an invalid rendering was refused:\n%s", cf.theirs, served)
	}

	vm.destroysBehindCaddy(t, bootstrap, stack, cf)
}
