package vps_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/traefik"
)

const (
	startYourProxy = "Start your proxy on 80 and 443 now"
	moveApplyWait  = 15 * time.Minute
)

type prompted struct {
	line string
	once sync.Once
	told chan struct{}
}

func promptedBy(line string) *prompted { return &prompted{line: line, told: make(chan struct{})} }

func (p *prompted) Say(message string) {
	if message == p.line {
		p.once.Do(func() { close(p.told) })
	}
}

func (p *prompted) Warn(string) {}

func (p *prompted) Error(string) {}

func (p *prompted) Detail(string) {}

func (p *prompted) Debug(string) {}

func (p *prompted) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

func (vm machine) bootstrapsBehind(t *testing.T, proxy *vps.Proxy) provider.Bootstrap {
	t.Helper()
	p := vm.provider(t, func(o *vps.Options) { o.Proxy = proxy })
	t.Cleanup(func() { closing(t, p) })
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	return bootstrap
}

func (vm machine) recordReads(t *testing.T) string {
	t.Helper()
	return vm.ssh(t, "sudo cat "+quote(host.FrontRecordPath))
}

func servingOne(t *testing.T, vm machine, proxy *vps.Proxy, hostname string) (*vps.Provider, edge.EdgeStack) {
	t.Helper()
	ctx := context.Background()
	fixtures(t, vm)
	d := vm.deployingBehind(t, proxy)
	opened, err := d.Edges().Open(edge.None, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := edge.StackSpec{Version: "test", Tier: environment.TierProduction, Slug: frontedSlug}
	stack, err := opened.Reconcile(ctx, spec, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	promotes(t, d, stack, "p-one", "one", provisioned(t, d, "one"), 1)
	recorded(t, d, frontedSlug, stack.State())
	if refused := bindsThrough(t, d, hostname); refused != "" {
		t.Fatalf("AddHostname(%s) = %s", hostname, refused)
	}
	t.Cleanup(func() {
		fronted, err := vm.deployingBehind(t, proxy).Edges().Open(edge.None, nil)
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := fronted.Reconcile(context.Background(), spec, stack.State())
		if err != nil {
			t.Fatalf("Reconcile() behind the proxy the box ends behind = %v", err)
		}
		if err := reopened.Destroy(context.Background()); err != nil {
			t.Errorf("Destroy() = %v", err)
		}
		vm.ssh(t, "sudo docker ps -aq --filter label="+host.LabelApp+" | xargs -r sudo docker rm -f >/dev/null 2>&1 || true")
	})
	return d, stack
}

func TestLiveABoxMovesFromOcelsOwnProxyToAnNginxAndBack(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	t.Cleanup(func() { vm.purges(t) })
	ctx := context.Background()
	production := provider.BootstrapRequest{Tier: environment.TierProduction, WrittenBy: "live-suite"}

	if err := vm.bootstrapsBehind(t, nil).Apply(ctx, production, nil); err != nil {
		t.Fatalf("Apply() under ocel's own proxy = %v", err)
	}
	ours, _ := servingOne(t, vm, nil, frontedHostname)
	vm.routesThrough(t, frontedHostname, "one")

	nginx := frontProxy(t, "nginx")
	toNginx := vm.bootstrapsBehind(t, nginx)
	told := promptedBy(startYourProxy)
	moved := make(chan error, 1)
	go func() { moved <- toNginx.Apply(ctx, production, told) }()
	select {
	case <-told.told:
	case err := <-moved:
		t.Fatalf("the move to nginx ended with %v before it asked for nginx to be started", err)
	case <-time.After(moveApplyWait):
		t.Fatalf("the move to nginx never asked for nginx to be started within %s", moveApplyWait)
	}
	if state := vm.state(t, caddy.Container); state != "gone" {
		t.Errorf("%s is %s when the move asks for nginx, want it gone so nginx can take 80 and 443", caddy.Container, state)
	}
	vm.fronted(t, "nginx", "*.localhost", "*."+frontedPreview)
	select {
	case err := <-moved:
		if err != nil {
			t.Fatalf("Apply() moving to nginx = %v", err)
		}
	case <-time.After(moveApplyWait):
		t.Fatalf("the move to nginx did not finish within %s of nginx starting", moveApplyWait)
	}
	if record := vm.recordReads(t); !strings.Contains(record, `"manual":{"port":8480`) {
		t.Errorf("%s reads %s after the move, want nginx recorded as a proxy you route yourself", host.FrontRecordPath, record)
	}
	if vm.exists(t, host.ProxyData) {
		t.Errorf("%s outlived the move off ocel's own proxy", host.ProxyData)
	}
	vm.routesThrough(t, frontedHostname, "one")

	err := ours.PreflightDeploy(ctx, provider.DeployPreflight{Deploy: provider.DeploySpec{
		Slug: frontedSlug, Tier: environment.TierProduction, Apps: []provider.AppEntry{{App: liveApp, Image: fixtureAt("one")}},
	}})
	if said := refused(t, err, refusal.CodeInvalid); !strings.Contains(said.Message, "add `\"proxy\": \"manual\"`") {
		t.Errorf("a deploy still under ocel's own proxy was refused with %q, want the value to write", said.Message)
	}

	back := vm.bootstrapsBehind(t, nil)
	_, err = back.Plan(ctx, production)
	if said := refused(t, err, refusal.CodeNotReady); !strings.Contains(said.Message, "nginx") || !strings.Contains(said.Message, "takes over") {
		t.Errorf("the move back while nginx holds 80 and 443 was refused with %q, want nginx named as what to stop", said.Message)
	}
	vm.ssh(t, "sudo systemctl stop nginx")
	if err := back.Apply(ctx, production, nil); err != nil {
		t.Fatalf("Apply() moving back to ocel's own proxy = %v", err)
	}
	if !vm.running(t, caddy.Container) {
		t.Errorf("%s is not running after the move back", caddy.Container)
	}
	if record := vm.recordReads(t); !strings.Contains(record, `"proxy":null`) {
		t.Errorf("%s reads %s after the move back, want ocel's own proxy recorded", host.FrontRecordPath, record)
	}
	vm.routesThrough(t, frontedHostname, "one")
}

func TestLiveABoxMovesOcelsFileToAnotherDirectoryYourTraefikWatchesWithoutDroppingARequest(t *testing.T) {
	const bound = "shop.p1306.test"
	vm := liveMachine(t)
	vm.purges(t)
	t.Cleanup(func() { vm.purges(t) })
	vm.fronted(t, "traefik")
	ctx := context.Background()
	production := provider.BootstrapRequest{Tier: environment.TierProduction, WrittenBy: "live-suite"}

	proxy := frontProxy(t, "traefik")
	if err := vm.bootstrapsBehind(t, proxy).Apply(ctx, production, nil); err != nil {
		t.Fatalf("Apply() behind your Traefik = %v", err)
	}
	d, _ := servingOne(t, vm, proxy, bound)
	vm.routesThrough(t, bound, "one")
	servedByTheBox(t, d, bound)

	from := proxy.Traefik.Directory
	to := from + "/ocel"
	vm.ssh(t, "sudo install -d -m 0755 "+quote(to))
	moving := *proxy.Traefik
	moving.Directory = to
	moved := &vps.Proxy{Traefik: &moving}

	boardID := "sudo docker inspect --type container --format '{{.Id}}' " + quote(host.SwitchboardContainer)
	board := vm.ssh(t, boardID)
	vm.loads(t, bound)
	vm.awaitAnswers(t, "of one before the move", counting("one", 0))
	started := vm.clock(t)
	if err := vm.bootstrapsBehind(t, moved).Apply(ctx, production, nil); err != nil {
		t.Fatalf("Apply() moving ocel's file to %s = %v", to, err)
	}
	finished := vm.clock(t)
	proxy.Traefik = moved.Traefik
	vm.awaitAnswers(t, "of one after the move", counting("one", finished))
	heard := vm.stopsLoad(t)
	var during int
	for _, answer := range heard {
		if !strings.HasPrefix(answer.code, "2") {
			t.Errorf("a request through your Traefik asked at %.3f answered %s while ocel's file moved between %.3f and %.3f", answer.at, answer.code, started, finished)
		}
		if answer.at >= started && answer.at <= finished {
			during++
		}
	}
	if during == 0 {
		t.Errorf("no request was asked while ocel's file moved between %.3f and %.3f, so the %d answered prove nothing about the move", started, finished, len(heard))
	}
	if after := vm.ssh(t, boardID); after != board {
		t.Errorf("%s is container %s after the move and was %s before, want it left running: a switchboard started again drops what it serves", host.SwitchboardContainer, after, board)
	}
	if vm.exists(t, quote(from+"/"+traefik.FileName)) {
		t.Errorf("%s/%s outlived the move to %s", from, traefik.FileName, to)
	}
	if !vm.exists(t, quote(to+"/"+traefik.FileName)) {
		t.Errorf("the move placed no %s in %s", traefik.FileName, to)
	}
	if record := vm.recordReads(t); !strings.Contains(record, `"directory":"`+to+`"`) {
		t.Errorf("%s reads %s after the move, want %s recorded", host.FrontRecordPath, record, to)
	}
}
