package vps_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (vm machine) ssh(t *testing.T, command string) string {
	t.Helper()
	return vm.sshAs(t, vm.user, command)
}

func (vm machine) sshAs(t *testing.T, login, command string) string {
	t.Helper()
	rendered, err := vm.attempt(login, command)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			t.Fatalf("ssh %s@%s %q: %v: %s", login, vm.addr, command, err, strings.TrimSpace(string(exit.Stderr)))
		}
		t.Fatalf("ssh %s@%s %q: %v", login, vm.addr, command, err)
	}
	return rendered
}

func (vm machine) attempt(login, command string) (string, error) {
	return vm.feeding(login, "", command)
}

func (vm machine) authenticates(login, command string) (string, error) {
	return vm.dialling(login, "", command, "-o", "ControlPath=none")
}

func (vm machine) feeding(login, fed, command string) (string, error) {
	return vm.dialling(login, fed, command)
}

func (vm machine) dialling(login, fed, command string, extra ...string) (string, error) {
	args := append([]string{
		"-F", vm.config, "-i", vm.key,
		"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
	}, extra...)
	ran := exec.Command("ssh", append(args, login+"@"+vm.addr, command)...)
	ran.Stdin = strings.NewReader(fed)
	rendered, err := ran.Output()
	var exited *exec.ExitError
	if errors.As(err, &exited) && len(exited.Stderr) > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exited.Stderr)))
	}
	return string(rendered), err
}

func TestLiveBootstrapWritesTheTiersAndASecondRunPlansNothing(t *testing.T) {
	vm := live(t)
	vm.purges(t)
	p := vm.provider(t)
	defer closing(t, p)

	ctx := context.Background()
	class := providerkit.ClassProduction
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}

	fresh, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() of a machine nothing has bootstrapped = %v", err)
	}
	if fresh.Present {
		t.Fatal("Describe() claims a bootstrap on a machine nothing has written to")
	}

	req := providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: fresh.Held}
	plan, err := bootstrapper.Plan(ctx, req)
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	group := onlyGroup(t, plan)
	if want := "vps/" + vm.user + "@" + vm.addr; group.Name != want {
		t.Errorf("Plan() named the group %q, want %q", group.Name, want)
	}
	if group.Action != providerkit.ActionCreate {
		t.Errorf("Plan() against a fresh machine plans %q, want %q", group.Action, providerkit.ActionCreate)
	}
	for _, want := range []string{
		"/etc/ocel/production",
		"/var/lib/ocel/production",
		"/var/lib/ocel/production/records",
		"/var/lib/ocel/.ssh/authorized_keys",
		"/usr/local/lib/ocel",
		"/usr/local/lib/ocel/records",
		deployLogin,
	} {
		if planned := planFor(group, want); planned.Action != providerkit.ActionCreate {
			t.Errorf("Plan() shows %s as %q, want it created", want, planned.Action)
		}
	}

	if err := bootstrapper.Apply(ctx, req, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}

	stamp := stampOn(t, vm)
	if stamp.State != host.StateComplete {
		t.Errorf("the stamp reads state %q after an apply that finished, want %q", stamp.State, host.StateComplete)
	}
	if stamp.Schema != providerkit.BootstrapSchema {
		t.Errorf("the stamp reads schema %d, want %d", stamp.Schema, providerkit.BootstrapSchema)
	}
	if stamp.Writer != "live-suite" {
		t.Errorf("the stamp reads writer %q, want the writer that applied it", stamp.Writer)
	}
	for _, item := range host.Items(class, nil, host.ArchAMD64) {
		if stamp.Digests[item.ID()] == "" {
			t.Errorf("the stamp carries no digest for %s, and nothing can say whether it drifted", item.ID())
		}
	}
	if owner := strings.TrimSpace(vm.ssh(t, "stat -c %U /etc/ocel/production")); owner != "root" {
		t.Errorf("/etc/ocel/production is owned by %q, want root: the class tier is root's alone", owner)
	}
	if owner := strings.TrimSpace(vm.ssh(t, "sudo stat -c %U /var/lib/ocel/production/records")); owner != deployLogin {
		t.Errorf("the record tier is owned by %q, want %s: it is the deploy login's alone", owner, deployLogin)
	}
	standsAsDecided(t, vm)

	standing, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() after Apply() = %v", err)
	}
	if !standing.Present || !standing.Stacks[0].DigestCurrent {
		t.Fatalf("Describe() after Apply() = %+v, want a present bootstrap standing at the digest applied, %s\n%s",
			standing.Stacks, stillMoving(t, bootstrapper, class, standing.Held), vm.proxySaid(t))
	}
	again, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: standing.Held})
	if err != nil {
		t.Fatalf("a second Plan() = %v", err)
	}
	repeat := onlyGroup(t, again)
	if repeat.Action != providerkit.ActionKeep {
		t.Errorf("a second Plan() over a bootstrapped machine plans %q, want %q", repeat.Action, providerkit.ActionKeep)
	}
	for _, change := range repeat.Changes {
		if change.Action != providerkit.ActionKeep {
			t.Errorf("a second Plan() shows %s as %q, want it kept", change.Name, change.Action)
		}
	}

	removal, err := bootstrapper.PlanRemoval(ctx, class)
	if err != nil {
		t.Fatalf("PlanRemoval() = %v", err)
	}
	leaving := onlyGroup(t, removal)
	if planFor(leaving, "/var/lib/ocel/production").Reason == "" {
		t.Error("PlanRemoval() names the state directory with no reason, and the typed confirmation must say what is unrecoverable")
	}
	if last := leaving.Changes[len(leaving.Changes)-1]; last.Name != "/etc/ocel" {
		t.Errorf("PlanRemoval() ends at %s, want the shared root taken after every class tier beneath it", last.Name)
	}

	if err := bootstrapper.Remove(ctx, class, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	gone, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() after Remove() = %v", err)
	}
	if gone.Present {
		t.Error("Describe() still claims a bootstrap after Remove()")
	}
	for _, path := range []string{"/etc/ocel", "/var/lib/ocel", "/usr/local/lib/ocel"} {
		if left := strings.TrimSpace(vm.ssh(t, "test -e "+path+" && echo standing || echo gone")); left != "gone" {
			t.Errorf("%s is %s after Remove(), and a destroy leaves no bytes behind", path, left)
		}
	}
	if left := strings.TrimSpace(vm.ssh(t, "getent passwd "+deployLogin+" || true")); left != "" {
		t.Errorf("%s still stands as %q after Remove(), and a login nothing deploys as is a login nobody revokes", deployLogin, left)
	}
	if err := bootstrapper.Remove(ctx, class, nil); err != nil {
		t.Errorf("a second Remove() = %v, want an already-forgotten target to be a no-op", err)
	}
}

func TestLiveAnUnfinishedApplyIsReportedAsDrifted(t *testing.T) {
	vm := live(t)
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
	defer func() {
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	vm.ssh(t, `sudo sed -i 's/"state": "complete"/"state": "applying"/' /etc/ocel/production/stamp.json`)

	described, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() over an unfinished apply = %v", err)
	}
	if !described.Present {
		t.Fatal("Describe() reads no bootstrap where a stamp stands, and an unfinished apply still stamped the host")
	}
	if described.Stacks[0].DigestCurrent {
		t.Error("Describe() calls an unfinished apply current, so a partially applied host reads as a healthy one")
	}
	plan, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: described.Held})
	if err != nil {
		t.Fatalf("Plan() over an unfinished apply = %v", err)
	}
	if action := onlyGroup(t, plan).Action; action != providerkit.ActionUpdate {
		t.Errorf("Plan() over an unfinished apply plans %q, want %q", action, providerkit.ActionUpdate)
	}
}

func TestLiveApplyRefusesWorkTheShownPlanNeverCarried(t *testing.T) {
	vm := live(t)
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
	defer func() {
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	shown, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	vm.ssh(t, "sudo rm -rf /usr/local/lib/ocel")

	err = bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: shown.Held}, nil)
	if err == nil {
		t.Fatal("Apply() did work the plan the user consented to never carried")
	}
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Apply() over a host that moved = %v, want a refusal the CLI can render as a re-plan", err)
	}
}

func TestLiveForgettingARecordNothingWroteIsAlreadyForgotten(t *testing.T) {
	p := live(t).provider(t)
	defer closing(t, p)

	name := providerkit.RecordName{providerkit.RootConformance, string(providerkit.ClassProduction), t.Name()}
	if err := providerkit.Forget(context.Background(), p.Records(), name); err != nil {
		t.Fatalf("Forget() of a record nothing wrote = %v, want cleanup idempotent from the store's point of view", err)
	}
}

func onlyGroup(t *testing.T, plan providerkit.Plan) providerkit.ChangeGroup {
	t.Helper()
	if len(plan.Groups) != 1 {
		t.Fatalf("the plan carries %d groups, want the one core group this provider stands up", len(plan.Groups))
	}
	return plan.Groups[0]
}

func (vm machine) proxySaid(t *testing.T) string {
	t.Helper()

	state := vm.inspects(t, "container", host.ProxyContainer,
		"{{.State.Status}} exit={{.State.ExitCode}} restarts={{.RestartCount}} error={{.State.Error}}")
	if state == "" {
		return host.ProxyContainer + " stands on this host as nothing the engine knows about"
	}
	return host.ProxyContainer + " is " + state + ", and it said:\n" +
		vm.ssh(t, "sudo docker logs --tail 15 "+host.ProxyContainer+" 2>&1 || true")
}

func planFor(group providerkit.ChangeGroup, name string) providerkit.Change {
	for _, change := range group.Changes {
		if change.Name == name {
			return change
		}
	}
	return providerkit.Change{}
}

func stampOn(t *testing.T, vm machine) host.Stamp {
	t.Helper()
	var stamp host.Stamp
	if err := json.Unmarshal([]byte(vm.ssh(t, "sudo cat /etc/ocel/production/stamp.json")), &stamp); err != nil {
		t.Fatalf("the stamp this host carries is not one ocel can read: %v", err)
	}
	return stamp
}
