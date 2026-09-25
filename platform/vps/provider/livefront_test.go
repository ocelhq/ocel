package vps_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const frontsDir = "../../../tests/fronts"

const (
	frontedSlug     = "shop"
	frontedHostname = "shop.localhost"
	frontedPreview  = "preview.localhost"
)

func frontScript(t *testing.T, front, step string) []byte {
	t.Helper()
	script, err := os.ReadFile(filepath.Join(frontsDir, front, step))
	if err != nil {
		t.Fatalf("read the %s step of the %s front: %v", step, front, err)
	}
	return script
}

func (vm machine) fronted(t *testing.T, front string, names ...string) {
	t.Helper()
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, quote(name))
	}
	down := frontScript(t, front, "down.sh")
	t.Cleanup(func() { vm.feeds(t, "sudo sh -s", down) })
	vm.feeds(t, "sudo sh -s -- "+strings.Join(quoted, " "), frontScript(t, front, "up.sh"))
}

func (vm machine) frontUntouched(t *testing.T, front string) {
	t.Helper()
	vm.feeds(t, "sudo sh -s", frontScript(t, front, "check.sh"))
}

func (vm machine) throughTheFront(t *testing.T, hostname, path string) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t, "curl -sSk -m 10 --resolve "+quote(hostname+":443:127.0.0.1")+" "+quote("https://"+hostname+path)))
}

func routingByHand(o *vps.Options) { o.Proxy = &vps.Proxy{Manual: &vps.Manual{}} }

func (vm machine) deployingBehind(t *testing.T) *vps.Provider {
	t.Helper()
	p := vps.NewProvider(vps.Options{
		SSH:   vps.Target{Host: vm.addr, User: deployLogin, IdentityFile: vm.key, Config: vm.config},
		Proxy: &vps.Proxy{Manual: &vps.Manual{}},
	})
	t.Cleanup(func() { closing(t, p) })
	return p
}

func TestLiveOcelServesBehindAnNginxItNeverWritesTo(t *testing.T) {
	vm := live(t)
	vm.purges(t)
	t.Cleanup(func() { vm.purges(t) })
	vm.fronted(t, "nginx", "*.localhost", "*."+frontedPreview)

	ctx := context.Background()
	class := providerkit.ClassProduction
	p := vm.provider(t, routingByHand)
	defer closing(t, p)
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() behind nginx = %v", err)
	}
	if state := vm.state(t, caddy.Container); state != "gone" {
		t.Errorf("%s is %s on a box nginx fronts, want it never stood", caddy.Container, state)
	}
	if published := vm.inspects(t, "container", host.SwitchboardContainer, "{{json .HostConfig.PortBindings}}"); !strings.Contains(published, `"HostIp":"127.0.0.1","HostPort":"8480"`) {
		t.Errorf("the switchboard publishes %s, want 127.0.0.1:8480 for nginx to reach", published)
	}
	if record := vm.ssh(t, "sudo cat "+quote(host.FrontRecordPath)); !strings.Contains(record, `"manual":{"port":8480}`) {
		t.Errorf("%s reads %s, want the manual proxy recorded", host.FrontRecordPath, record)
	}

	fixtures(t, vm)
	d := vm.deployingBehind(t)
	if err := d.PreflightDeploy(ctx, providerkit.DeployPreflight{Plan: providerkit.DeployPlan{
		Slug: frontedSlug, Class: class, Apps: []providerkit.AppEntry{{App: liveApp, Image: fixtureAt("one")}},
	}}); err != nil {
		t.Fatalf("PreflightDeploy() behind nginx = %v", err)
	}

	opened, err := d.Edges().Open(boxedge.Kind)
	if err != nil {
		t.Fatal(err)
	}
	stack, err := opened.Reconcile(ctx, edge.StackSpec{Version: "test", Class: edge.ClassProduction, Slug: frontedSlug}, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	promotes(t, stack, "p-one", "one", standsUp(t, d, "one"), 1)
	recorded(t, d, frontedSlug, stack.State())

	added, err := overTheContract(t, d).AddHostname(ctx, &contractv1.HostnameRequest{
		Slug:       frontedSlug,
		Configured: configuredHosts(frontedHostname),
		Edge:       &contractv1.EdgeSelection{Kind: string(boxedge.Kind)},
	})
	if err != nil {
		t.Fatalf("AddHostname() = %v", err)
	}
	var said []string
	for added.Receive() {
		event := added.Msg()
		if line := event.GetProgress().GetMessage(); line != "" {
			said = append(said, line)
		}
		if line := event.GetLog().GetMessage(); line != "" {
			said = append(said, line)
		}
		if done := event.GetResult(); done != nil && !done.GetSuccess() {
			t.Fatalf("AddHostname() behind nginx = %s", done.GetError())
		}
	}
	if err := added.Err(); err != nil {
		t.Fatalf("AddHostname() stream = %v", err)
	}
	if route := manual.Route(frontedHostname, manual.DefaultPort); !slices.Contains(said, route) {
		t.Errorf("the bind said %q, want %q", said, route)
	}
	if served := vm.throughTheFront(t, frontedHostname, "/"); served != "one" {
		t.Fatalf("nginx answered %q for %s, want the release it routes to the switchboard", served, frontedHostname)
	}
	if kind, err := d.Serving(ctx, boxedge.Kind, frontedHostname); err != nil || kind != boxedge.Kind {
		t.Errorf("Serving(%s) = %q, %v, want the box proven through nginx", frontedHostname, kind, err)
	}

	two := standsUp(t, d, "two")
	var hammered string
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		hammered = vm.ssh(t, "for i in $(seq 1 150); do curl -sk -m 10 -o /dev/null -w '%{http_code} ' --resolve "+
			quote(frontedHostname+":443:127.0.0.1")+" "+quote("https://"+frontedHostname+"/")+"; sleep 0.05; done")
	}()
	promotes(t, stack, "p-two", "two", two, 2)
	group.Wait()
	for _, code := range strings.Fields(hammered) {
		if !strings.HasPrefix(code, "2") {
			t.Errorf("a request through nginx answered %s while the release flipped:\n%s", code, hammered)
			break
		}
	}
	if served := vm.throughTheFront(t, frontedHostname, "/"); served != "two" {
		t.Errorf("nginx answered %q after the redeploy, want two", served)
	}

	if err := d.Host().InstallPreviewEntry(ctx, frontedPreview); err != nil {
		t.Fatalf("InstallPreviewEntry(%s) = %v", frontedPreview, err)
	}
	probe := edge.ProbeHostname(edge.PreviewWildcard(frontedPreview))
	if answered, err := d.Host().ServedEdge(ctx, probe); err != nil || answered.Edge != switchboard.EdgeName {
		t.Errorf("ServedEdge(%s) = %+v, %v, want the preview catch-all answered by the box through nginx", probe, answered, err)
	}

	checks, err := d.CheckStanding(ctx, providerkit.StandingRequest{Class: class})
	if err != nil {
		t.Fatalf("CheckStanding() = %v", err)
	}
	for _, check := range checks {
		if check.Verdict == providerkit.StandingFail {
			t.Errorf("%s fails behind nginx: %s", check.Subject, check.Finding)
		}
	}

	if err := d.Host().RemovePreviewEntry(ctx, frontedPreview); err != nil {
		t.Errorf("RemovePreviewEntry() = %v", err)
	}
	if err := stack.Destroy(ctx); err != nil {
		t.Errorf("Destroy() = %v", err)
	}
	vm.ssh(t, "sudo docker ps -aq --filter label="+host.LabelApp+" | xargs -r sudo docker rm -f >/dev/null 2>&1 || true")
	if err := bootstrapper.Remove(ctx, class, nil); err != nil {
		t.Fatalf("Remove() behind nginx = %v", err)
	}
	if state := vm.state(t, host.SwitchboardContainer); state != "gone" {
		t.Errorf("%s is %s after the box was destroyed", host.SwitchboardContainer, state)
	}
	if held := vm.ssh(t, "sudo test -e "+quote(host.FrontRecordPath)+" && echo held || echo gone"); strings.TrimSpace(held) != "gone" {
		t.Errorf("%s outlived the destroy", host.FrontRecordPath)
	}
	vm.frontUntouched(t, "nginx")
}
