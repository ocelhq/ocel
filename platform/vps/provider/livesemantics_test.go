package vps_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	helperDir     = "/usr/local/lib/ocel"
	recordsHelper = helperDir + "/records"
	recordsDir    = "/var/lib/ocel/production/records"
)

type killer struct {
	at   string
	kill context.CancelFunc
}

func (k *killer) Say(message string) {
	if message == k.at {
		k.kill()
	}
}

func (k *killer) Detail(string) {}

func (k *killer) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

type sayings []string

func (s *sayings) Say(message string) { *s = append(*s, message) }

func (s *sayings) Detail(string) {}

func (s *sayings) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

func refused(t *testing.T, err error, code providerkit.Code) providerkit.Refusal {
	t.Helper()

	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a refusal the CLI can render", err)
	}
	if refusal.Code != code {
		t.Fatalf("refusal = %v, want one the CLI renders as %q", err, code)
	}
	return refusal
}

func mode(t *testing.T, vm machine, path string) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t, "sudo stat -c %a "+path))
}

type planning interface {
	Plan(context.Context, providerkit.BootstrapRequest) (providerkit.Plan, error)
}

func stillMoving(t *testing.T, planner planning, class providerkit.Class, held any) string {
	t.Helper()

	plan, err := planner.Plan(context.Background(),
		providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: held})
	if err != nil {
		return "and a re-plan over it said " + err.Error()
	}
	var moving []string
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Action != providerkit.ActionKeep {
				moving = append(moving, change.Kind+" "+change.Name+" plans as "+string(change.Action))
			}
		}
	}
	if len(moving) == 0 {
		return "and a re-plan over it moves nothing, so the stamp and the survey disagree over what is recorded rather than over what stands"
	}
	return "and a re-plan moves " + strings.Join(moving, ", ")
}

func TestLiveAnApplyKilledMidWayIsFinishedByTheSameCommand(t *testing.T) {
	vm := live(t)
	vm.ssh(t, "sudo rm -rf /etc/ocel /var/lib/ocel "+helperDir)
	vm.ssh(t, "sudo userdel "+deployLogin+" 2>/dev/null || true")

	p := vm.provider(t)
	defer closing(t, p)
	ctx := context.Background()
	class := providerkit.ClassProduction
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	dying, kill := context.WithCancel(ctx)
	defer kill()
	err = bootstrapper.Apply(dying, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"},
		&killer{at: "wrote " + host.KindFile + " " + host.SealHelper, kill: kill})
	if err == nil {
		t.Fatal("the apply ran to completion, and a half-applied host is what this proves recovery from")
	}

	if stamp := stampOn(t, vm); stamp.State != host.StateApplying {
		t.Fatalf("the stamp reads state %q after an apply that died, want %q", stamp.State, host.StateApplying)
	}
	described, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() over a half-applied host = %v", err)
	}
	if !described.Present {
		t.Fatal("Describe() reads no bootstrap where a stamp stands, and the apply stamped this host before its first item")
	}
	if described.Stacks[0].DigestCurrent {
		t.Error("Describe() calls a half-applied host current, so it would be mistaken for a healthy one")
	}
	if !described.Unfinished {
		t.Error("Describe() says nothing about the apply that never finished, so status has nothing to banner and reads the host as ordinarily stale")
	}

	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Heal: true}, nil); err == nil {
		t.Error("heal finished an apply it did not start")
	} else if refusal := refused(t, err, providerkit.CodeDenied); !strings.Contains(refusal.Message, host.StampPath(class)) {
		t.Errorf("heal over a half-applied host says %q, want it to name the stamp that says so", refusal.Message)
	}

	plan, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: described.Held})
	if err != nil {
		t.Fatalf("Plan() over a half-applied host = %v", err)
	}
	group := onlyGroup(t, plan)
	if group.Action != providerkit.ActionUpdate {
		t.Errorf("Plan() over a half-applied host plans %q, want %q", group.Action, providerkit.ActionUpdate)
	}
	for _, name := range []string{helperDir, host.SealHelper} {
		if planned := planFor(group, name); planned.Action != providerkit.ActionKeep {
			t.Errorf("Plan() shows %s as %q, want the work the dead apply already did left alone", name, planned.Action)
		}
	}
	for _, name := range []string{deployLogin, "/var/lib/ocel/production", recordsDir} {
		if planned := planFor(group, name); planned.Action != providerkit.ActionCreate {
			t.Errorf("Plan() shows %s as %q, want the work that is left", name, planned.Action)
		}
	}

	var said sayings
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: described.Held}, &said); err != nil {
		t.Fatalf("the same command over a half-applied host = %v, want recovery to be the first run's command", err)
	}
	standing := host.KindFile + " " + host.SealHelper + ": already current"
	if !slices.Contains(said, standing) {
		t.Errorf("the apply said %q, want %q: what the plan showed as a no-op is declared rather than passed over", said, standing)
	}
	if stamp := stampOn(t, vm); stamp.State != host.StateComplete {
		t.Errorf("the stamp reads state %q after the run that finished it, want %q", stamp.State, host.StateComplete)
	}
	finished, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	if !finished.Stacks[0].DigestCurrent {
		t.Errorf("Describe() still reads the host as drifted after the apply that finished it, %s",
			stillMoving(t, bootstrapper, class, finished.Held))
	}
	if finished.Unfinished {
		t.Error("Describe() still banners the host as half-applied after the apply that finished it")
	}
}

func TestLiveAnUnattendedApplyInstallsWhatIsAbsentAndStopsAtWhatStands(t *testing.T) {
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

	vm.ssh(t, "sudo rm -rf /etc/ocel /var/lib/ocel "+helperDir+" /etc/sudoers.d/ocel-seal")
	vm.ssh(t, "sudo userdel "+deployLogin+" 2>/dev/null || true")
	unattended := providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Unattended: true}
	if err := bootstrapper.Apply(ctx, unattended, nil); err != nil {
		t.Fatalf("an unattended apply over a machine carrying none of ocel's own state = %v, want absent-to-present to proceed", err)
	}

	vm.ssh(t, "sudo chmod 700 "+helperDir)
	converging, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	unattended.Held = converging.Held
	if err := bootstrapper.Apply(ctx, unattended, nil); err != nil {
		t.Fatalf("an unattended apply over a host whose %s has moved mode = %v, want a converge that destroys nothing to proceed", helperDir, err)
	}
	if held := mode(t, vm, helperDir); held != "755" {
		t.Errorf("%s stands at %q after the unattended converge, want 755", helperDir, held)
	}

	vm.ssh(t, "sudo chmod 700 "+recordsHelper)
	moved, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	unattended.Held = moved.Held
	refusal := refused(t, bootstrapper.Apply(ctx, unattended, nil), providerkit.CodeNotReady)
	if !strings.Contains(refusal.Message, recordsHelper) {
		t.Errorf("the refusal says %q, want it to name %s as what it would write over", refusal.Message, recordsHelper)
	}
	if held := mode(t, vm, recordsHelper); held != "700" {
		t.Errorf("%s stands at %q after the apply that refused it, want the refusal to have written nothing", recordsHelper, held)
	}

	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: moved.Held}, nil); err != nil {
		t.Fatalf("the same apply with somebody there to accept it = %v", err)
	}
	if held := mode(t, vm, recordsHelper); held != "755" {
		t.Errorf("%s stands at %q after the apply that accepted the write, want 755", recordsHelper, held)
	}
}

func TestLiveHealReassertsTheStateTierAndRefusesEverythingBesideWhole(t *testing.T) {
	vm := live(t)
	class := providerkit.ClassProduction
	p := bootstrapped(t, vm, class)
	ctx := context.Background()
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	healing := providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Heal: true, Unattended: true}

	vm.ssh(t, "sudo chmod 700 "+recordsDir)
	if err := bootstrapper.Apply(ctx, healing, nil); err != nil {
		t.Fatalf("heal over a drifted record tier = %v, want the state the deploy login owns reasserted", err)
	}
	if held := mode(t, vm, recordsDir); held != "750" {
		t.Errorf("%s stands at %q after a heal, want 750", recordsDir, held)
	}

	vm.ssh(t, "sudo chmod 700 "+recordsDir)
	vm.ssh(t, "sudo chmod 700 "+helperDir)
	refusal := refused(t, bootstrapper.Apply(ctx, healing, nil), providerkit.CodeDenied)
	if !strings.Contains(refusal.Message, helperDir) {
		t.Errorf("heal over a mixed set says %q, want it to name %s as what heal may not write", refusal.Message, helperDir)
	}
	if held := mode(t, vm, recordsDir); held != "700" {
		t.Errorf("%s stands at %q after a heal that refused, want a mixed set refused whole rather than half-done", recordsDir, held)
	}

	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("the apply that may write both = %v", err)
	}
	for path, want := range map[string]string{recordsDir: "750", helperDir: "755"} {
		if held := mode(t, vm, path); held != want {
			t.Errorf("%s stands at %q after the apply that may write it, want %q", path, held, want)
		}
	}
}

func TestLiveASymlinkWhereTheDeployLoginOwnsAPathIsRefusedRatherThanChowned(t *testing.T) {
	vm := live(t)
	class := providerkit.ClassProduction
	p := bootstrapped(t, vm, class)
	ctx := context.Background()
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}

	vm.sshAs(t, deployLogin, "rmdir "+recordsDir+" && ln -s /etc "+recordsDir)
	defer vm.ssh(t, "sudo rm -f "+recordsDir+" && sudo install -d -m 750 -o "+deployLogin+" -g "+deployLogin+" "+recordsDir)

	refusal := refused(t, bootstrapper.Apply(ctx,
		providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Heal: true, Unattended: true}, nil),
		providerkit.CodeDenied)
	if !strings.Contains(refusal.Message, recordsDir) || !strings.Contains(refusal.Message, "/etc") {
		t.Errorf("heal over a path the deploy login pointed elsewhere says %q, want both the path and where it points named", refusal.Message)
	}
	if held := strings.TrimSpace(vm.ssh(t, "sudo stat -c %U /etc")); held != "root" {
		t.Fatalf("/etc is owned by %q after a heal that followed a link into it, want root", held)
	}
}

func TestLiveHealAsTheDeployLoginReassertsItsOwnTierAndNothingBeside(t *testing.T) {
	vm := live(t)
	class := providerkit.ClassProduction
	bootstrapped(t, vm, class)

	bootstrapper, err := vm.deploying(t).Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	healing := providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Heal: true, Unattended: true}

	vm.sshAs(t, deployLogin, "chmod 700 "+recordsDir)
	if err := bootstrapper.Apply(ctx, healing, nil); err != nil {
		t.Fatalf("heal as %s over its own drifted record tier = %v, want what that login owns reasserted without asking for root", deployLogin, err)
	}
	if held := mode(t, vm, recordsDir); held != "750" {
		t.Errorf("%s stands at %q after a heal driven by the login that owns it, want 750", recordsDir, held)
	}

	vm.sshAs(t, deployLogin, "chmod 700 "+recordsDir)
	vm.ssh(t, "sudo chmod 700 "+helperDir)
	defer vm.ssh(t, "sudo chmod 755 "+helperDir)
	refusal := refused(t, bootstrapper.Apply(ctx, healing, nil), providerkit.CodeDenied)
	if !strings.Contains(refusal.Message, helperDir) {
		t.Errorf("heal as %s over a mixed set says %q, want %s named as what that login may not write", deployLogin, refusal.Message, helperDir)
	}
	if held := mode(t, vm, recordsDir); held != "700" {
		t.Errorf("%s stands at %q after a heal that refused, want a mixed set refused whole rather than half-done", recordsDir, held)
	}
}
