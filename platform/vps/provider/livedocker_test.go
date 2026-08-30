package vps_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	engineName = "docker"
	unitName   = "docker.service"
	socketName = "docker.socket"
	initScript = "/etc/init.d/" + engineName
	holding    = "/var/tmp/ocel-live-away"
)

func purged(t *testing.T, vm machine) {
	t.Helper()
	vm.ssh(t, "sudo systemctl disable --now "+socketName+" "+unitName+" >/dev/null 2>&1 || true")
	vm.ssh(t, "sudo DEBIAN_FRONTEND=noninteractive apt-get purge -y "+
		"docker-ce docker-ce-cli docker-ce-rootless-extras containerd.io docker-buildx-plugin docker-compose-plugin docker.io "+
		">/dev/null 2>&1 || true")
	vm.ssh(t, "sudo rm -rf /var/lib/docker /var/lib/containerd /etc/docker")
	if stood := strings.TrimSpace(vm.ssh(t, "command -v "+engineName+" || true")); stood != "" {
		t.Fatalf("%s still stands at %q, so the absent-engine case cannot be proven on this machine", engineName, stood)
	}
}

func carrying(t *testing.T, vm machine) []string {
	t.Helper()
	var paths []string
	for _, line := range strings.Split(vm.ssh(t, "systemctl show -p FragmentPath,SourcePath,DropInPaths "+unitName), "\n") {
		_, named, split := strings.Cut(strings.TrimSpace(line), "=")
		if split {
			paths = append(paths, strings.Fields(named)...)
		}
	}
	return append(paths, strings.Fields(vm.ssh(t, "for p in "+initScript+" /etc/systemd/system/"+unitName+"; do [ -e \"$p\" ] && echo \"$p\"; done; true"))...)
}

func unmade(t *testing.T, vm machine) ([]string, string) {
	t.Helper()
	vm.ssh(t, "sudo systemctl stop "+socketName+" "+unitName+" >/dev/null 2>&1 || true")
	var moved []string
	for range 4 {
		var fresh bool
		for _, path := range carrying(t, vm) {
			if slices.Contains(moved, path) {
				continue
			}
			if away := strings.TrimSpace(vm.ssh(t, "if [ -e "+path+" ]; then sudo mkdir -p \"$(dirname "+holding+path+")\" && sudo mv "+path+" "+holding+path+" && echo "+path+"; fi")); away != "" {
				moved, fresh = append(moved, away), true
			}
		}
		vm.ssh(t, "sudo systemctl daemon-reload")
		if _, err := vm.attempt(vm.user, "systemctl cat "+unitName); err != nil {
			return moved, ""
		}
		if !fresh {
			break
		}
	}
	return moved, vm.ssh(t, "systemctl cat "+unitName+" 2>&1 | head -30; "+
		"systemctl show -p Names,LoadState,UnitFileState,FragmentPath,SourcePath,DropInPaths "+unitName+"; "+
		"ls -l /etc/init.d /etc/systemd/system /run/systemd/generator /run/systemd/generator.early /run/systemd/generator.late 2>&1 | head -60")
}

func TestLiveTheEngineIsInstalledOnConsentAndAnIdleDaemonIsOnlyStarted(t *testing.T) {
	vm := live(t)
	vm.ssh(t, "sudo rm -rf /etc/ocel /var/lib/ocel /usr/local/lib/ocel")
	purged(t, vm)

	p := vm.provider(t)
	defer closing(t, p)

	ctx := context.Background()
	class := providerkit.ClassProduction
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}

	absent, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	req := providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: absent.Held}
	plan, err := bootstrapper.Plan(ctx, req)
	if err != nil {
		t.Fatalf("Plan() over a machine with no engine = %v", err)
	}
	group := onlyGroup(t, plan)

	engine := planFor(group, engineName)
	if engine.Action != providerkit.ActionCreate {
		t.Fatalf("Plan() over a machine with no engine shows %s as %q, want the install a user consents to", engineName, engine.Action)
	}
	if !strings.Contains(engine.Reason, "https://get.docker.com") {
		t.Errorf("the engine is planned as %q, and the user consenting never learns what runs on their machine", engine.Reason)
	}
	if unit := planFor(group, unitName); unit.Action != providerkit.ActionCreate {
		t.Errorf("Plan() shows %s as %q on a machine that has no engine at all", unitName, unit.Action)
	}
	var slow bool
	for _, change := range group.Changes {
		if change.Slow {
			slow = true
			continue
		}
		if slow {
			t.Errorf("Plan() puts %s after the slow work, and the minutes-long change closes the plan", change.Name)
		}
	}

	if err := bootstrapper.Apply(ctx, req, nil); err != nil {
		t.Fatalf("Apply() over a machine with no engine = %v", err)
	}
	defer func() {
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	if active := strings.TrimSpace(vm.ssh(t, "systemctl is-active "+unitName)); active != "active" {
		t.Errorf("%s is %q after an apply that installed it, want a daemon that serves", unitName, active)
	}
	if groups := vm.ssh(t, "id -nG "+deployLogin); !strings.Contains(groups, engineName) {
		t.Errorf("%s is in %q, and a deploy that cannot reach the engine socket deploys nothing", deployLogin, strings.TrimSpace(groups))
	}
	if _, err := vm.attempt(deployLogin, "docker version --format '{{.Server.Version}}'"); err != nil {
		t.Errorf("%s cannot reach the daemon this bootstrap installed: %v", deployLogin, err)
	}

	standing, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	if !standing.Stacks[0].DigestCurrent {
		t.Errorf("Describe() calls a machine that has just been bootstrapped, engine and all, drifted, %s\n%s",
			stillMoving(t, bootstrapper, class, standing.Held), vm.proxySaid(t))
	}
	again, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: standing.Held})
	if err != nil {
		t.Fatal(err)
	}
	settled := onlyGroup(t, again)
	if settled.Action != providerkit.ActionKeep {
		t.Errorf("a re-run over a fully bootstrapped machine plans %q for the stack, want nothing left to do", settled.Action)
	}
	for _, change := range settled.Changes {
		if change.Action != providerkit.ActionKeep {
			t.Errorf("a re-run plans %q for %s, and bootstrap is a statement of state rather than a stack of side effects.\n%s",
				change.Action, change.Name, vm.proxySaid(t))
		}
	}

	written := strings.TrimSpace(vm.ssh(t, "stat -c %Y \"$(command -v "+engineName+")\""))
	if held := strings.TrimSpace(vm.ssh(t, "systemctl is-enabled "+socketName+" 2>/dev/null || true")); held == "enabled" {
		defer vm.ssh(t, "sudo systemctl enable --now "+socketName)
	}
	vm.ssh(t, "sudo systemctl disable --now "+socketName+" "+unitName)

	idle, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	stopped := providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: idle.Held}
	restarting, err := bootstrapper.Plan(ctx, stopped)
	if err != nil {
		t.Fatalf("Plan() over an installed engine whose daemon is idle = %v", err)
	}
	waking := onlyGroup(t, restarting)
	if kept := planFor(waking, engineName); kept.Action != providerkit.ActionKeep {
		t.Errorf("an idle daemon plans %q for %s, want the engine kept: presence is never remediated by a second install", kept.Action, engineName)
	}
	if unit := planFor(waking, unitName); unit.Action != providerkit.ActionUpdate {
		t.Errorf("an idle daemon plans %q for %s, want the unit enabled", unit.Action, unitName)
	}
	for _, change := range waking.Changes {
		if change.Kind == host.KindNetwork || change.Kind == host.KindVolume || change.Kind == host.KindContainer || change.Name == unitName {
			continue
		}
		if change.Action != providerkit.ActionKeep {
			t.Errorf("stopping the daemon re-planned %s as %q, and nothing but the unit and what the daemon answers for moved", change.Name, change.Action)
		}
	}

	if err := bootstrapper.Apply(ctx, stopped, nil); err != nil {
		t.Fatalf("Apply() over an idle daemon = %v", err)
	}
	if active := strings.TrimSpace(vm.ssh(t, "systemctl is-active "+unitName)); active != "active" {
		t.Errorf("%s is %q after the apply that was meant to start it", unitName, active)
	}
	if now := strings.TrimSpace(vm.ssh(t, "stat -c %Y \"$(command -v "+engineName+")\"")); now != written {
		t.Errorf("the engine binary was written again to start a daemon that was already installed, at %s where it stood at %s", now, written)
	}

	if unitPath := strings.TrimSpace(vm.ssh(t, "systemctl show -p FragmentPath --value "+unitName)); unitPath == "" {
		t.Fatalf("%s has no fragment on a machine this bootstrap installed docker onto", unitName)
	}
	moved, surviving := unmade(t, vm)
	defer func() {
		for i := len(moved) - 1; i >= 0; i-- {
			vm.ssh(t, "if [ -e "+holding+moved[i]+" ]; then sudo mv "+holding+moved[i]+" "+moved[i]+"; fi")
		}
		vm.ssh(t, "sudo rm -rf "+holding)
		vm.ssh(t, "sudo systemctl daemon-reload")
		vm.ssh(t, "sudo systemctl enable --now "+unitName)
	}()
	if surviving != "" {
		t.Fatalf("%s still stands with %s moved aside, so a docker binary with no unit behind it cannot be proven on this machine. The machine says:\n%s",
			unitName, strings.Join(moved, ", "), surviving)
	}

	shimmed, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	reinstalling, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: shimmed.Held})
	if err != nil {
		t.Fatalf("Plan() over a docker binary with no unit behind it = %v", err)
	}
	shimming := onlyGroup(t, reinstalling)
	if engine := planFor(shimming, engineName); engine.Action != providerkit.ActionUpdate {
		t.Errorf("a binary with no %s plans %q for the engine, want the install shown over what stands: keeping it leaves an apply enabling a unit the machine does not carry, on every run, forever", unitName, engine.Action)
	}
	if unit := planFor(shimming, unitName); unit.Action != providerkit.ActionCreate {
		t.Errorf("a binary with no %s plans %q for the unit, want the install that brings one", unitName, unit.Action)
	}
	refusal := refused(t, bootstrapper.Apply(ctx,
		providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: shimmed.Held, Unattended: true}, nil),
		providerkit.CodeNotReady)
	if !strings.Contains(refusal.Message, engineName) {
		t.Errorf("an unattended apply over a docker binary with no unit says %q, want it refused by name: nobody is there to consent to %s being run as root over an install that already stands",
			refusal.Message, "https://get.docker.com")
	}
}
