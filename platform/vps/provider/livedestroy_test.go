package vps_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const workload = "ocel-live-workload"

type said struct{ lines []string }

func (s *said) Say(message string)    { s.lines = append(s.lines, message) }
func (s *said) Detail(message string) { s.lines = append(s.lines, message) }

func (s *said) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

func (s *said) at(fragment string) int {
	return slices.IndexFunc(s.lines, func(line string) bool { return strings.Contains(line, fragment) })
}

func (s *said) removed(kind, path string) int {
	return slices.Index(s.lines, "removed "+kind+" "+path)
}

func (vm machine) stands(t *testing.T, path string) bool {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t, "sudo test -e "+path+" && echo standing || echo gone")) == "standing"
}

func (vm machine) running(t *testing.T, container string) bool {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t, "sudo docker inspect -f '{{.State.Running}}' "+container+" 2>/dev/null || echo gone")) == "true"
}

func (vm machine) runs(t *testing.T, container string) {
	t.Helper()
	vm.ssh(t, "sudo docker rm -f "+container+" >/dev/null 2>&1 || true")
	vm.ssh(t, "sudo docker run -d --name "+container+" busybox sleep 900")
	if !vm.running(t, container) {
		t.Fatalf("%s is not running, so what a destroy does to a user's workloads cannot be proven on this machine", container)
	}
}

func TestLiveDestroyTakesTheStampLastAndLeavesTheEngineAndTheTrustStore(t *testing.T) {
	vm := live(t)
	vm.ssh(t, "sudo rm -rf /etc/ocel /var/lib/ocel /usr/local/lib/ocel")
	p := vm.provider(t)
	defer closing(t, p)

	ctx := context.Background()
	class := providerkit.ClassProduction
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	vm.runs(t, workload)
	defer vm.ssh(t, "sudo docker rm -f "+workload+" >/dev/null 2>&1 || true")

	removal, err := bootstrapper.PlanRemoval(ctx, class)
	if err != nil {
		t.Fatalf("PlanRemoval() = %v", err)
	}
	leaving := onlyGroup(t, removal)
	if want := "vps/" + vm.user + "@" + vm.addr; leaving.Name != want {
		t.Errorf("PlanRemoval() named the group %q, want %q", leaving.Name, want)
	}
	for _, bearing := range []string{host.StateDir(class), host.SealKeyPath(class)} {
		if reason := planFor(leaving, bearing).Reason; reason == "" {
			t.Errorf("PlanRemoval() takes %s with no reason, and the typed confirmation must name what is unrecoverable before a user types", bearing)
		}
	}
	if kept := planFor(leaving, "docker"); kept.Action != providerkit.ActionKeep {
		t.Errorf("PlanRemoval() plans the engine as %q, want it kept: removing ocel is not removing what the host runs", kept.Action)
	}

	before, err := os.ReadFile(vm.known)
	if err != nil {
		t.Fatal(err)
	}

	report := &said{}
	if err := bootstrapper.Remove(ctx, class, report); err != nil {
		t.Fatalf("Remove() = %v", err)
	}

	stamped := report.removed(host.KindDir, host.ClassDir(class))
	if stamped < 0 {
		t.Fatalf("Remove() never says it took %s, and the stamp goes with it:\n%s", host.ClassDir(class), strings.Join(report.lines, "\n"))
	}
	for kind, earlier := range map[string]string{
		host.KindDir:     host.StateDir(class),
		host.KindSealKey: host.SealKeyPath(class),
		host.KindFile:    host.SealHelper,
		host.KindUser:    deployLogin,
	} {
		if at := report.removed(kind, earlier); at < 0 || at > stamped {
			t.Errorf("Remove() took %s %s at line %d and the class directory at %d, and the stamp is what an interrupted destroy leaves behind",
				kind, earlier, at, stamped)
		}
	}
	if note := report.at("ssh-keygen -R"); note < 0 {
		t.Errorf("Remove() never spells the line that drops this host from known_hosts:\n%s", strings.Join(report.lines, "\n"))
	}

	after, err := os.ReadFile(vm.known)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("known_hosts changed under a destroy, and ocel never edits the trust store its user owns:\nbefore %q\nafter  %q", before, after)
	}

	if active := strings.TrimSpace(vm.ssh(t, "systemctl is-active docker.service || true")); active != "active" {
		t.Errorf("docker.service is %q after a destroy, want a daemon that still serves the workloads on this host", active)
	}
	if !vm.running(t, workload) {
		t.Errorf("%s is gone after a destroy, and removing ocel took a container ocel never ran", workload)
	}
	if group := strings.TrimSpace(vm.ssh(t, "getent group docker || true")); group == "" {
		t.Error("the docker group went with the destroy, and the group belongs to the engine that stays")
	}
}

func TestLiveTheSingletonsStandWhileASiblingClassDoesAndGoWithTheLast(t *testing.T) {
	vm := live(t)
	vm.ssh(t, "sudo rm -rf /etc/ocel /var/lib/ocel /usr/local/lib/ocel")
	vm.ssh(t, "sudo userdel "+deployLogin+" 2>/dev/null || true")
	p := vm.provider(t)
	defer closing(t, p)

	ctx := context.Background()
	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	for _, class := range []providerkit.Class{production, preview} {
		if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
			t.Fatalf("Apply(%s) = %v", class, err)
		}
	}
	vm.runs(t, workload)
	defer vm.ssh(t, "sudo docker rm -f "+workload+" >/dev/null 2>&1 || true")

	singletons := []string{"/var/lib/ocel", "/usr/local/lib/ocel", "/usr/local/lib/ocel/seal", "/usr/local/lib/ocel/records",
		host.ProxyHelper, host.ProxyConfig, "/etc/sudoers.d/ocel-seal", "/etc/ocel"}

	first, err := bootstrapper.PlanRemoval(ctx, production)
	if err != nil {
		t.Fatalf("PlanRemoval(%s) = %v", production, err)
	}
	beside := onlyGroup(t, first)
	for _, singleton := range append(slices.Clone(singletons), deployLogin) {
		if planned := planFor(beside, singleton); planned.Action == providerkit.ActionDelete {
			t.Errorf("destroying %s plans %s as %q while %s still stands on this host", production, singleton, planned.Action, preview)
		}
	}

	if err := bootstrapper.Remove(ctx, production, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", production, err)
	}
	for _, singleton := range singletons {
		if !vm.stands(t, singleton) {
			t.Errorf("%s went with the %s class, and %s is a tenant of this machine that still deploys through it", singleton, production, preview)
		}
	}
	if entry := strings.TrimSpace(vm.ssh(t, "getent passwd "+deployLogin+" || true")); entry == "" {
		t.Errorf("%s went with the %s class, and the %s sibling deploys as it", deployLogin, production, preview)
	}
	for _, gone := range []string{host.ClassDir(production), host.StateDir(production)} {
		if vm.stands(t, gone) {
			t.Errorf("%s stands after the class that owns it was destroyed", gone)
		}
	}
	standing, err := bootstrapper.Describe(ctx, preview)
	if err != nil {
		t.Fatalf("Describe(%s) after destroying its sibling = %v", preview, err)
	}
	if len(standing.Stacks) != 1 {
		t.Fatalf("Describe(%s) carries %d stacks after its sibling was destroyed, want the one this host stands up", preview, len(standing.Stacks))
	}
	if !standing.Present || !standing.Stacks[0].DigestCurrent {
		t.Errorf("Describe(%s) = %+v after its sibling was destroyed, want a class untouched by a destroy beside it", preview, standing.Stacks)
	}
	if !vm.running(t, workload) {
		t.Errorf("%s is gone after the first destroy", workload)
	}
	if !vm.running(t, host.ProxyContainer) {
		t.Errorf("%s went with the %s class, and the %s sibling is still served through it", host.ProxyContainer, production, preview)
	}

	last, err := bootstrapper.PlanRemoval(ctx, preview)
	if err != nil {
		t.Fatalf("PlanRemoval(%s) = %v", preview, err)
	}
	alone := onlyGroup(t, last)
	for _, singleton := range append(slices.Clone(singletons), deployLogin) {
		if planned := planFor(alone, singleton); planned.Action != providerkit.ActionDelete {
			t.Errorf("destroying the last class plans %s as %q, and a singleton nothing uses is one nobody revokes", singleton, planned.Action)
		}
	}
	if err := bootstrapper.Remove(ctx, preview, nil); err != nil {
		t.Fatalf("Remove(%s) = %v", preview, err)
	}
	for _, singleton := range singletons {
		if vm.stands(t, singleton) {
			t.Errorf("%s stands after the last class on this host was destroyed", singleton)
		}
	}
	if entry := strings.TrimSpace(vm.ssh(t, "getent passwd "+deployLogin+" || true")); entry != "" {
		t.Errorf("%s still stands as %q after the last class went", deployLogin, entry)
	}

	if active := strings.TrimSpace(vm.ssh(t, "systemctl is-active docker.service || true")); active != "active" {
		t.Errorf("docker.service is %q after both classes went, want the engine ocel never prunes", active)
	}
	if !vm.running(t, workload) {
		t.Errorf("%s is gone after the last destroy, and removing ocel from a host removed the workloads on it", workload)
	}
	if err := bootstrapper.Remove(ctx, preview, nil); err != nil {
		t.Errorf("a second Remove(%s) = %v, want an already-forgotten target to succeed", preview, err)
	}
}
