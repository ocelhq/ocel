package vps_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
	frontedPointer  = "pr-7"
)

const (
	loadDir    = "/tmp/ocel-front-load"
	loadLoops  = 4
	loadSettle = 10
	loadWait   = 60 * time.Second
)

const loadScript = `dir=` + loadDir + `
while [ ! -e "$dir/stop" ]; do
    at=$(date +%s.%N)
    said=$(curl -sk -m 10 -w ' %{http_code}' --resolve "$2:443:127.0.0.1" "https://$2/" || true)
    printf '%s %s\n' "$at" "$said" >> "$dir/$1"
done
`

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

func (vm machine) clock(t *testing.T) float64 {
	t.Helper()
	read := strings.TrimSpace(vm.ssh(t, "date +%s.%N"))
	at, err := strconv.ParseFloat(read, 64)
	if err != nil {
		t.Fatalf("the box read its clock as %q: %v", read, err)
	}
	return at
}

type answered struct {
	at   float64
	body string
	code string
}

func (vm machine) loads(t *testing.T, hostname string) {
	t.Helper()
	vm.ssh(t, "rm -rf "+loadDir+" && mkdir -p "+loadDir)
	vm.feeds(t, "cat > "+loadDir+"/loop.sh", []byte(loadScript))
	t.Cleanup(func() { vm.ssh(t, "touch "+loadDir+"/stop") })
	vm.ssh(t, "for n in $(seq 1 "+strconv.Itoa(loadLoops)+"); do setsid sh "+loadDir+"/loop.sh \"$n\" "+quote(hostname)+
		" </dev/null >/dev/null 2>&1 & done")
}

func (vm machine) answers(t *testing.T) []answered {
	t.Helper()
	var read []answered
	for _, line := range lines(vm.ssh(t, "cat "+loadDir+"/[0-9]* 2>/dev/null || true")) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		at, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			t.Fatalf("a load line reads %q: %v", line, err)
		}
		read = append(read, answered{at: at, body: strings.Join(fields[1:len(fields)-1], " "), code: fields[len(fields)-1]})
	}
	return read
}

func (vm machine) awaitAnswers(t *testing.T, what string, count func([]answered) int) {
	t.Helper()
	deadline := time.Now().Add(loadWait)
	for count(vm.answers(t)) < loadSettle {
		if time.Now().After(deadline) {
			t.Fatalf("the load never read %d answers %s within %s: %v", loadSettle, what, loadWait, vm.answers(t))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (vm machine) stopsLoad(t *testing.T) []answered {
	t.Helper()
	vm.ssh(t, "touch "+loadDir+"/stop")
	deadline := time.Now().Add(loadWait)
	for strings.TrimSpace(vm.ssh(t, "pgrep -f "+quote(loadDir+"/loop[.]sh")+" >/dev/null && echo running || echo stopped")) != "stopped" {
		if time.Now().After(deadline) {
			t.Fatalf("the load loops still run %s after they were told to stop", loadWait)
		}
		time.Sleep(200 * time.Millisecond)
	}
	return vm.answers(t)
}

func counting(body string, from float64) func([]answered) int {
	return func(read []answered) int {
		held := 0
		for _, answer := range read {
			if answer.body == body && answer.at > from {
				held++
			}
		}
		return held
	}
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
	servesBehind(t, "nginx")
}

func TestLiveOcelServesBehindAnNginxInAContainerOnItsNetwork(t *testing.T) {
	vm := servesBehind(t, "nginx-container")
	if networks := vm.inspects(t, "container", "ocel-front-nginx", "{{range $name, $_ := .NetworkSettings.Networks}}{{$name}} {{end}}"); !slices.Contains(strings.Fields(networks), host.ProxyNetwork) {
		t.Errorf("the front container sits on %q after ocel came and went, want it still on %s: ocel never moves a proxy it does not run", networks, host.ProxyNetwork)
	}
}

func servesBehind(t *testing.T, front string) machine {
	t.Helper()
	vm := liveMachine(t)
	vm.purges(t)
	t.Cleanup(func() { vm.purges(t) })
	vm.fronted(t, front, "*.localhost", "*."+frontedPreview)

	ctx := context.Background()
	p := vm.provider(t, routingByHand)
	defer closing(t, p)
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, WrittenBy: "live-suite"}, nil); err != nil {
			t.Fatalf("Apply(%s) behind %s = %v", class, front, err)
		}
	}
	if state := vm.state(t, caddy.Container); state != "gone" {
		t.Errorf("%s is %s on a box %s fronts, want it never stood", caddy.Container, state, front)
	}
	if published := vm.inspects(t, "container", host.SwitchboardContainer, "{{json .HostConfig.PortBindings}}"); !strings.Contains(published, `"HostIp":"127.0.0.1","HostPort":"8480"`) {
		t.Errorf("the switchboard publishes %s, want 127.0.0.1:8480 for a proxy on the box to reach", published)
	}
	if record := vm.ssh(t, "sudo cat "+quote(host.FrontRecordPath)); !strings.Contains(record, `"manual":{"port":8480}`) {
		t.Errorf("%s reads %s, want the manual proxy recorded", host.FrontRecordPath, record)
	}

	fixtures(t, vm)
	d := vm.deployingBehind(t)
	if err := d.PreflightDeploy(ctx, providerkit.DeployPreflight{Plan: providerkit.DeployPlan{
		Slug: frontedSlug, Class: providerkit.ClassProduction, Apps: []providerkit.AppEntry{{App: liveApp, Image: fixtureAt("one")}},
	}}); err != nil {
		t.Fatalf("PreflightDeploy() behind %s = %v", front, err)
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

	bindsBehind(t, d, front)
	if served := vm.throughTheFront(t, frontedHostname, "/"); served != "one" {
		t.Fatalf("%s answered %q for %s, want the release it routes to the switchboard", front, served, frontedHostname)
	}
	if kind, err := d.ServingEdge(ctx, boxedge.Kind, frontedHostname); err != nil || kind != boxedge.Kind {
		t.Errorf("Serving(%s) = %q, %v, want the box proven through %s", frontedHostname, kind, err, front)
	}

	two := standsUp(t, d, "two")
	vm.loads(t, frontedHostname)
	vm.awaitAnswers(t, "of one before the flip", counting("one", 0))
	flipping := vm.clock(t)
	promotes(t, stack, "p-two", "two", two, 2)
	flipped := vm.clock(t)
	vm.awaitAnswers(t, "of two after the flip", counting("two", flipped))
	heard := vm.stopsLoad(t)
	var during int
	for _, answer := range heard {
		if !strings.HasPrefix(answer.code, "2") {
			t.Errorf("a request through %s asked at %.3f answered %s while the release flipped between %.3f and %.3f", front, answer.at, answer.code, flipping, flipped)
		}
		if answer.at >= flipping && answer.at <= flipped {
			during++
		}
		if answer.at > flipped && answer.body != "two" {
			t.Errorf("a request through %s asked at %.3f, after the flip finished at %.3f, answered %q, want two", front, answer.at, flipped, answer.body)
		}
	}
	if during == 0 {
		t.Errorf("no request through %s was asked while the release flipped between %.3f and %.3f, so the %d answered prove nothing about the flip", front, flipping, flipped, len(heard))
	}

	servesAPreviewBehind(t, vm, d, opened, front)

	checks, err := d.CheckHost(ctx, providerkit.HostCheckRequest{Class: providerkit.ClassProduction})
	if err != nil {
		t.Fatalf("CheckStanding() = %v", err)
	}
	for _, check := range checks {
		if check.Verdict == providerkit.HostFail {
			t.Errorf("%s fails behind %s: %s", check.Subject, front, check.Finding)
		}
	}

	if err := stack.Destroy(ctx); err != nil {
		t.Errorf("Destroy() = %v", err)
	}
	vm.ssh(t, "sudo docker ps -aq --filter label="+host.LabelApp+" | xargs -r sudo docker rm -f >/dev/null 2>&1 || true")
	for _, class := range []providerkit.Class{providerkit.ClassPreview, providerkit.ClassProduction} {
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Fatalf("Remove(%s) behind %s = %v", class, front, err)
		}
	}
	if state := vm.state(t, host.SwitchboardContainer); state != "gone" {
		t.Errorf("%s is %s after the box was destroyed", host.SwitchboardContainer, state)
	}
	if held := vm.ssh(t, "sudo test -e "+quote(host.FrontRecordPath)+" && echo held || echo gone"); strings.TrimSpace(held) != "gone" {
		t.Errorf("%s outlived the destroy", host.FrontRecordPath)
	}
	vm.frontUntouched(t, front)
	return vm
}

func bindsBehind(t *testing.T, d *vps.Provider, front string) {
	t.Helper()
	added, err := overTheContract(t, d).AddHostname(context.Background(), &contractv1.HostnameRequest{
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
			t.Fatalf("AddHostname() behind %s = %s", front, done.GetError())
		}
	}
	if err := added.Err(); err != nil {
		t.Fatalf("AddHostname() stream = %v", err)
	}
	if route := manual.Route(frontedHostname, manual.DefaultPort); !slices.Contains(said, route) {
		t.Errorf("the bind said %q, want %q", said, route)
	}
}

func servesAPreviewBehind(t *testing.T, vm machine, d *vps.Provider, opened edge.Edge, front string) {
	t.Helper()
	ctx := context.Background()
	if _, err := opened.ReconcilePreviewWildcard(ctx, edge.PreviewWildcardSpec{
		BaseDomain: frontedPreview,
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
	}); err != nil {
		t.Fatalf("ReconcilePreviewWildcard(%s) behind %s = %v", frontedPreview, front, err)
	}
	previews, err := opened.Reconcile(ctx, edge.StackSpec{Version: "test", Class: edge.ClassPreview, Slug: frontedSlug},
		edge.StackState{GlobalPreview: frontedPreview})
	if err != nil {
		t.Fatalf("Reconcile(preview): %v", err)
	}
	promotesPreview(t, d, previews, frontedSlug, liveApp, fixtureAt("one"), frontedPointer, 1)

	hostname := edge.SharedPreview(frontedSlug, frontedPreview).Hosts(frontedPointer, []string{liveApp})[0]
	if served := vm.throughTheFront(t, hostname, "/"); served != "one" {
		t.Errorf("%s answered %q for the preview %s, want the release it was promoted to", front, served, hostname)
	}
	probe := edge.ProbeHostname(edge.PreviewWildcard(frontedPreview))
	if said, err := d.Host().ServedEdge(ctx, probe); err != nil || said.Edge != switchboard.EdgeName {
		t.Errorf("ServedEdge(%s) = %+v, %v, want the preview catch-all answered by the box through %s", probe, said, err, front)
	}

	if err := previews.Destroy(ctx); err != nil {
		t.Errorf("Destroy(preview) = %v", err)
	}
	if err := opened.DestroyPreviewWildcard(ctx, frontedPreview); err != nil {
		t.Errorf("DestroyPreviewWildcard(%s) = %v", frontedPreview, err)
	}
}
