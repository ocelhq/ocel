package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	boxedge "github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func TestLiveASwitchboardAPruneTookWithItsNetworkIsStoodAgainByTheNextDeployAndServes(t *testing.T) {
	const front = "nginx"
	vm := liveMachine(t)
	vm.purges(t)
	t.Cleanup(func() { vm.purges(t) })
	vm.fronted(t, front, "*.localhost")

	ctx := context.Background()
	p := vm.provider(t, routingByHand)
	defer closing(t, p)
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, providerkit.BootstrapRequest{Class: providerkit.ClassProduction, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) behind %s = %v", providerkit.ClassProduction, front, err)
	}
	t.Cleanup(func() {
		vm.ssh(t, "sudo docker ps -aq --filter label="+host.LabelApp+" | xargs -r sudo docker rm -f >/dev/null 2>&1 || true")
		if err := bootstrap.Remove(ctx, providerkit.ClassProduction, nil); err != nil {
			t.Errorf("Remove(%s) = %v", providerkit.ClassProduction, err)
		}
	})

	fixtures(t, vm)
	d := vm.deployingBehind(t)
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
		t.Fatalf("%s answered %q for %s before the prune, want the release it routes to the switchboard", front, served, frontedHostname)
	}

	vm.ssh(t, "sudo docker stop "+host.SwitchboardContainer+" >/dev/null")
	vm.ssh(t, "sudo docker container prune -f >/dev/null && sudo docker network prune -f >/dev/null")
	if state := vm.state(t, host.SwitchboardContainer); state != "gone" {
		t.Fatalf("%s is %s after the prune, and a switchboard the prune left proves nothing about standing it again", host.SwitchboardContainer, state)
	}
	if held := strings.TrimSpace(vm.ssh(t, "sudo docker network inspect "+quote(host.ProxyNetwork)+" >/dev/null 2>&1 && echo held || echo gone")); held != "gone" {
		t.Fatalf("the %s network is %s after the prune, want it gone with the switchboard as a host tool's nightly cleanup leaves it", host.ProxyNetwork, held)
	}

	if err := d.PreflightDeploy(ctx, providerkit.DeployPreflight{Plan: providerkit.DeployPlan{
		Slug: frontedSlug, Class: providerkit.ClassProduction, Apps: []providerkit.AppEntry{{App: liveApp, Image: fixtureAt("one")}},
	}}); err != nil {
		t.Fatalf("PreflightDeploy() after the prune = %v, want the switchboard and its network stood again from what bootstrap left", err)
	}
	if served := vm.throughTheFront(t, frontedHostname, "/"); served != "one" {
		t.Fatalf("%s answered %q for %s after the deploy stood the switchboard again, want the release the table still routes", front, served, frontedHostname)
	}
	promotes(t, stack, "p-two", "two", standsUp(t, d, "two"), 2)
	if served := vm.throughTheFront(t, frontedHostname, "/"); served != "two" {
		t.Errorf("%s answered %q for %s after a deploy onto the restored switchboard, want two", front, served, frontedHostname)
	}

	checks, err := d.CheckHost(ctx, providerkit.HostCheckRequest{Class: providerkit.ClassProduction})
	if err != nil {
		t.Fatalf("CheckHost() = %v", err)
	}
	reported := false
	for _, check := range checks {
		if check.Subject == host.SwitchboardContainer {
			reported = check.Verdict == providerkit.HostPass && strings.Contains(check.Finding, "a deploy stood it again")
			if !reported {
				t.Errorf("the doctor says %v %q of %s, want it passed and the restoration named", check.Verdict, check.Finding, host.SwitchboardContainer)
			}
		}
	}
	if !reported {
		t.Errorf("the doctor never reported on %s: %+v", host.SwitchboardContainer, checks)
	}

	if err := stack.Destroy(ctx); err != nil {
		t.Errorf("Destroy() = %v", err)
	}
}
