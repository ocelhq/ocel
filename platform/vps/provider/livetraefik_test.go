//go:build integration

package vps_test

import (
	"context"
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
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

type traefikFront struct {
	front     string
	bound     string
	preview   string
	theirs    string
	directory string
	network   string
	cleanup   bool
}

const traefikServeWait = 90 * time.Second

func TestLiveOcelServesBehindAStockTraefikItWritesOnlyItsOwnFileTo(t *testing.T) {
	servesBehindTraefik(t, liveMachine(t), traefikFront{
		front:     "traefik",
		bound:     "shop.p1306.test",
		preview:   "preview.p1306.test",
		theirs:    "mine.p1306.test",
		directory: "/etc/ocel-front-traefik/dynamic",
	})
}

func TestHostToolOcelServesBehindCoolifysTraefikThroughItsPreset(t *testing.T) {
	vm := hostTool(t, "coolify")
	servesBehindTraefik(t, vm, traefikFront{
		front:     "coolify-traefik",
		bound:     "shop.p1305.test",
		preview:   "preview.p1305.test",
		theirs:    "web.p1305.test",
		directory: "/data/coolify/proxy/dynamic",
		network:   "coolify",
		cleanup:   true,
	})
}

func TestHostToolOcelServesBehindDokploysTraefikThroughItsPreset(t *testing.T) {
	vm := hostTool(t, "dokploy")
	servesBehindTraefik(t, vm, traefikFront{
		front:     "dokploy-traefik",
		bound:     "shop.p1305.test",
		preview:   "preview.p1305.test",
		theirs:    "dapp.p1305.test",
		directory: "/etc/dokploy/traefik/dynamic",
		network:   "dokploy-network",
		cleanup:   true,
	})
}

func hostTool(t *testing.T, tool string) machine {
	t.Helper()
	if os.Getenv("OCEL_INCUS_ADDR") == "" {
		t.Fatal(unreachable)
	}
	vm := liveMachine(t)
	if strings.TrimSpace(vm.ssh(t, "sudo docker inspect --type container --format '{{.Name}}' "+quote(tool)+" 2>/dev/null || true")) == "" {
		t.Skipf("this box runs no %s; hand this test a clone of the #1305 %s box, as `incus copy p1305-%s/<snapshot> <name>` and `scripts/incus.sh info <name>`", tool, tool, tool)
	}
	return vm
}

func (vm machine) routesThrough(t *testing.T, hostname, want string) {
	t.Helper()
	deadline := time.Now().Add(traefikServeWait)
	for served := vm.throughTheFront(t, hostname, "/"); served != want; served = vm.throughTheFront(t, hostname, "/") {
		if time.Now().After(deadline) {
			t.Fatalf("%s answered %q through the Traefik within %s, want %q", hostname, served, traefikServeWait, want)
		}
		time.Sleep(time.Second)
	}
}

func servedByTheBox(t *testing.T, d *vps.Provider, hostname string) {
	t.Helper()
	deadline := time.Now().Add(traefikServeWait)
	for {
		said, err := d.Host().ProbeRouter(context.Background(), hostname)
		if err == nil && said.Router == switchboard.RouterKind {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ProbeRouter(%s) = %+v, %v within %s, want the box proven with the certificate your Traefik ordered", hostname, said, err, traefikServeWait)
		}
		time.Sleep(2 * time.Second)
	}
}

func bindsThrough(t *testing.T, d *vps.Provider, hostname string) string {
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

func unbindsThrough(t *testing.T, d *vps.Provider, hostname string) {
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

func servesBehindTraefik(t *testing.T, vm machine, tf traefikFront) {
	t.Helper()
	vm.purges(t)
	t.Cleanup(func() { vm.purges(t) })
	vm.fronted(t, tf.front)
	placed := tf.directory + "/" + traefik.FileName

	ctx := context.Background()
	proxy := frontProxy(t, tf.front)
	p := vm.provider(t, func(o *vps.Options) { o.Proxy = proxy })
	defer closing(t, p)
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, nil); err != nil {
			t.Fatalf("Apply(%s) behind %s = %v", tier, tf.front, err)
		}
	}
	if state := vm.state(t, caddy.Container); state != "gone" {
		t.Errorf("%s is %s on a box your Traefik fronts, want it never started", caddy.Container, state)
	}
	if tf.network != "" {
		if networks := frontNetworks(t, vm, host.SwitchboardContainer); !slices.Contains(networks, tf.network) {
			t.Errorf("%s sits on %q, want it on %s, where your Traefik reaches it", host.SwitchboardContainer, networks, tf.network)
		}
	} else if published := vm.inspects(t, "container", host.SwitchboardContainer, "{{json .HostConfig.PortBindings}}"); !strings.Contains(published, `"HostIp":"127.0.0.1","HostPort":"8480"`) {
		t.Errorf("the switchboard publishes %s, want 127.0.0.1:8480 for a Traefik on the host network to reach", published)
	}

	fixtures(t, vm)
	d := vm.deployingBehind(t, proxy)
	if err := d.PreflightDeploy(ctx, provider.DeployPreflight{Deploy: provider.DeploySpec{
		Slug: frontedSlug, Tier: environment.TierProduction, Apps: []provider.AppEntry{{App: liveApp, Image: fixtureAt("one")}},
	}}); err != nil {
		t.Fatalf("PreflightDeploy() behind %s = %v", tf.front, err)
	}
	opened, err := d.Edges().Open(edge.None, nil)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := opened.Reconcile(ctx, edge.StackSpec{Version: "test", Tier: environment.TierProduction, Slug: frontedSlug}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	promotes(t, d, stack, "p-one", "one", provisioned(t, d, "one"), 1)
	recorded(t, d, frontedSlug, stack.State())

	if refused := bindsThrough(t, d, tf.theirs); !strings.Contains(refused, "already routed by your Traefik") {
		t.Errorf("binding %s, which your Traefik already routes, = %q, want it refused naming the router", tf.theirs, refused)
	}
	if said := vm.ssh(t, "sudo cat "+quote(placed)+" 2>/dev/null || true"); strings.Contains(said, tf.theirs) {
		t.Errorf("%s names %s after its bind was refused:\n%s", placed, tf.theirs, said)
	}

	if refused := bindsThrough(t, d, tf.bound); refused != "" {
		t.Fatalf("AddHostname(%s) behind %s = %s", tf.bound, tf.front, refused)
	}
	vm.routesThrough(t, tf.bound, "one")
	servedByTheBox(t, d, tf.bound)
	if kind, err := d.ServingRouter(ctx, tf.bound); err != nil || kind != switchboard.RouterKind {
		t.Errorf("Serving(%s) = %q, %v, want the switchboard proven through %s", tf.bound, kind, err, tf.front)
	}

	two := provisioned(t, d, "two")
	vm.loads(t, tf.bound)
	vm.awaitAnswers(t, "of one before the flip", counting("one", 0))
	flipping := vm.clock(t)
	promotes(t, d, stack, "p-two", "two", two, 2)
	flipped := vm.clock(t)
	vm.awaitAnswers(t, "of two after the flip", counting("two", flipped))
	heard := vm.stopsLoad(t)
	var during int
	for _, answer := range heard {
		if !strings.HasPrefix(answer.code, "2") {
			t.Errorf("a request through %s asked at %.3f answered %s while the release flipped between %.3f and %.3f", tf.front, answer.at, answer.code, flipping, flipped)
		}
		if answer.at >= flipping && answer.at <= flipped {
			during++
		}
		if answer.at > flipped && answer.body != "two" {
			t.Errorf("a request through %s asked at %.3f, after the flip finished at %.3f, answered %q, want two", tf.front, answer.at, flipped, answer.body)
		}
	}
	if during == 0 {
		t.Errorf("no request through %s was asked while the release flipped between %.3f and %.3f, so the %d answered prove nothing about the flip", tf.front, flipping, flipped, len(heard))
	}

	servesAPreviewBehindTraefik(t, vm, d, opened, tf)

	if tf.cleanup {
		vm.ssh(t, "sudo docker stop "+host.SwitchboardContainer+" >/dev/null")
		vm.feeds(t, "sudo sh -s", frontScript(t, tf.front, "cleanup.sh"))
		if left := vm.ssh(t, "sudo docker network inspect "+host.ProxyNetwork+" >/dev/null 2>&1 && echo present || echo gone"); strings.TrimSpace(left) != "gone" {
			t.Fatalf("%s's cleanup left the %s network behind a stopped switchboard, so it proves nothing about recreating them", tf.front, host.ProxyNetwork)
		}
		if err := d.PreflightDeploy(ctx, provider.DeployPreflight{Deploy: provider.DeploySpec{
			Slug: frontedSlug, Tier: environment.TierProduction, Apps: []provider.AppEntry{{App: liveApp, Image: fixtureAt("two")}},
		}}); err != nil {
			t.Fatalf("PreflightDeploy() after %s's cleanup took the stopped switchboard = %v", tf.front, err)
		}
		vm.routesThrough(t, tf.bound, "two")
	}

	checks, err := d.CheckHost(ctx, provider.HostCheckRequest{Tier: environment.TierProduction})
	if err != nil {
		t.Fatalf("CheckHost() = %v", err)
	}
	for _, check := range checks {
		if check.Verdict == provider.HostFail {
			t.Errorf("%s fails behind %s: %s", check.Subject, tf.front, check.Finding)
		}
	}

	unbindsThrough(t, d, tf.bound)
	if said := vm.ssh(t, "sudo cat "+quote(placed)); strings.Contains(said, tf.bound) {
		t.Errorf("%s still names %s after the unbind:\n%s", placed, tf.bound, said)
	}
	if served := vm.throughTheFront(t, tf.bound, "/"); served == "two" {
		t.Errorf("%s still answers %q through your Traefik after the unbind", tf.bound, served)
	}

	if err := stack.Destroy(ctx); err != nil {
		t.Errorf("Destroy() = %v", err)
	}
	vm.ssh(t, "sudo docker ps -aq --filter label="+host.LabelApp+" | xargs -r sudo docker rm -f >/dev/null 2>&1 || true")
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		if err := bootstrap.Remove(ctx, tier, nil); err != nil {
			t.Fatalf("Remove(%s) behind %s = %v", tier, tf.front, err)
		}
	}
	if state := vm.state(t, host.SwitchboardContainer); state != "gone" {
		t.Errorf("%s is %s after the box was destroyed", host.SwitchboardContainer, state)
	}
	if left := vm.ssh(t, "sudo test -e "+quote(placed)+" && echo present || echo gone"); strings.TrimSpace(left) != "gone" {
		t.Errorf("%s outlived the destroy", placed)
	}
	vm.frontUntouched(t, tf.front)
}

func servesAPreviewBehindTraefik(t *testing.T, vm machine, d *vps.Provider, opened edge.Edge, tf traefikFront) {
	t.Helper()
	ctx := context.Background()
	if _, err := opened.ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{
		BaseDomain: tf.preview,
	}); err != nil {
		t.Fatalf("ReconcilePreviewWildcard(%s) behind %s = %v", tf.preview, tf.front, err)
	}
	previews, err := opened.Reconcile(ctx, edge.StackSpec{Version: "test", Tier: environment.TierPreview, Slug: frontedSlug},
		edge.StackState{GlobalPreview: tf.preview})
	if err != nil {
		t.Fatalf("Reconcile(preview): %v", err)
	}
	promotesPreview(t, d, previews, frontedSlug, liveApp, fixtureAt("one"), frontedPointer, 1)

	hostname := signLivePreviewHost(frontedSlug, tf.preview, frontedPointer, liveApp).Hostname
	vm.routesThrough(t, hostname, "one")
	servedByTheBox(t, d, edge.ProbeHostname(edge.PreviewWildcard(tf.preview)))

	if err := previews.Destroy(ctx); err != nil {
		t.Errorf("Destroy(preview) = %v", err)
	}
	if err := opened.DestroyPreviewWildcard(ctx, tf.preview); err != nil {
		t.Errorf("DestroyPreviewWildcard(%s) = %v", tf.preview, err)
	}
}
