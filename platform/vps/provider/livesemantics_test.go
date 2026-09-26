package vps_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func (k *killer) Span(string, time.Time, time.Time, error, ...edge.Attr) {}

type sayings []string

func (s *sayings) Say(message string) { *s = append(*s, message) }

func (s *sayings) Detail(string) {}

func (s *sayings) Span(string, time.Time, time.Time, error, ...edge.Attr) {}

func refused(t *testing.T, err error, code refusal.Code) refusal.Refusal {
	t.Helper()

	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("error = %v, want a refusal the CLI can render", err)
	}
	if refused.Code != code {
		t.Fatalf("refusal = %v, want one the CLI renders as %q", err, code)
	}
	return refused
}

func mode(t *testing.T, vm machine, path string) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t, "sudo stat -c %a "+path))
}

type planning interface {
	Plan(context.Context, provider.BootstrapRequest) (provider.Plan, error)
}

func stillMoving(t *testing.T, planner planning, class edge.Class, vendorState any) string {
	t.Helper()

	plan, err := planner.Plan(context.Background(),
		provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", VendorState: vendorState})
	if err != nil {
		return "and a re-plan over it said " + err.Error()
	}
	var moving []string
	for _, group := range plan.Groups {
		for _, change := range group.Changes {
			if change.Action.Writes() {
				moving = append(moving, change.Kind+" "+change.Name+" plans as "+string(change.Action))
			}
		}
	}
	if len(moving) == 0 {
		return "and a re-plan over it moves nothing, so the stamp and the survey disagree over what is recorded rather than over what exists"
	}
	return "and a re-plan moves " + strings.Join(moving, ", ")
}

func TestLiveAnApplyKilledMidWayIsFinishedByTheSameCommand(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	vm.forgetsTheDeployLogin(t)

	p := vm.provider(t)
	defer closing(t, p)
	ctx := context.Background()
	class := edge.ClassProduction
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := bootstrap.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	dying, kill := context.WithCancel(ctx)
	defer kill()
	err = bootstrap.Apply(dying, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite"},
		&killer{at: "wrote " + host.KindFile + " " + host.SealHelper, kill: kill})
	if err == nil {
		t.Fatal("the apply ran to completion, and a half-applied host is what this proves recovery from")
	}

	if stamp := stampOn(t, vm); stamp.State != host.StateApplying {
		t.Fatalf("the stamp reads state %q after an apply that died, want %q", stamp.State, host.StateApplying)
	}
	described, err := bootstrap.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() over a half-applied host = %v", err)
	}
	if !described.Present {
		t.Fatal("Describe() reads no bootstrap where a stamp exists, and the apply stamped this host before its first item")
	}
	if described.Stacks[0].DigestCurrent {
		t.Error("Describe() calls a half-applied host current, so it would be mistaken for a healthy one")
	}
	if !described.Unfinished {
		t.Error("Describe() says nothing about the apply that never finished, so status has nothing to banner and reads the host as ordinarily stale")
	}

	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", Heal: true}, nil); err == nil {
		t.Error("heal finished an apply it did not start")
	} else if got := refused(t, err, refusal.CodeDenied); !strings.Contains(got.Message, host.StampPath(class)) {
		t.Errorf("heal over a half-applied host says %q, want it to name the stamp that says so", got.Message)
	}

	plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", VendorState: described.VendorState})
	if err != nil {
		t.Fatalf("Plan() over a half-applied host = %v", err)
	}
	group := onlyGroup(t, plan)
	if group.Action != provider.ActionUpdate {
		t.Errorf("Plan() over a half-applied host plans %q, want %q", group.Action, provider.ActionUpdate)
	}
	for _, name := range []string{helperDir, host.SealHelper} {
		if planned := planFor(group, name); planned.Action != provider.ActionKeep {
			t.Errorf("Plan() shows %s as %q, want the work the dead apply already did left alone", name, planned.Action)
		}
	}
	for _, name := range []string{deployLogin, "/var/lib/ocel/production", recordsDir} {
		if planned := planFor(group, name); planned.Action != provider.ActionCreate {
			t.Errorf("Plan() shows %s as %q, want the work that is left", name, planned.Action)
		}
	}

	var said sayings
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", VendorState: described.VendorState}, &said); err != nil {
		t.Fatalf("the same command over a half-applied host = %v, want recovery to be the first run's command", err)
	}
	unchanged := host.KindFile + " " + host.SealHelper + ": already current"
	if !slices.Contains(said, unchanged) {
		t.Errorf("the apply said %q, want %q: what the plan showed as a no-op is declared rather than passed over", said, unchanged)
	}
	if stamp := stampOn(t, vm); stamp.State != host.StateComplete {
		t.Errorf("the stamp reads state %q after the run that finished it, want %q", stamp.State, host.StateComplete)
	}
	finished, err := bootstrap.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	if !finished.Stacks[0].DigestCurrent {
		t.Errorf("Describe() still reads the host as drifted after the apply that finished it, %s",
			stillMoving(t, bootstrap, class, finished.VendorState))
	}
	if finished.Unfinished {
		t.Error("Describe() still banners the host as half-applied after the apply that finished it")
	}
}

func TestLiveAReplacementRefusingApplyInstallsWhatIsAbsentAndStopsAtWhatExists(t *testing.T) {
	vm := liveMachine(t)
	p := vm.provider(t)
	defer closing(t, p)
	ctx := context.Background()
	class := edge.ClassProduction
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	defer func() {
		if err := bootstrap.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	vm.purges(t)
	vm.ssh(t, "sudo rm -f /etc/sudoers.d/ocel-seal-*")
	vm.forgetsTheDeployLogin(t)
	refusing := provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", RefuseReplacements: true}
	if err := bootstrap.Apply(ctx, refusing, nil); err != nil {
		t.Fatalf("an apply that refuses replacements over a machine with none of ocel's own state = %v, want absent-to-present to proceed", err)
	}

	vm.ssh(t, "sudo chmod 700 "+helperDir)
	converging, err := bootstrap.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	refusing.VendorState = converging.VendorState
	if err := bootstrap.Apply(ctx, refusing, nil); err != nil {
		t.Fatalf("an apply that refuses replacements over a host whose %s has moved mode = %v, want a converge that destroys nothing to proceed", helperDir, err)
	}
	if got := mode(t, vm, helperDir); got != "755" {
		t.Errorf("%s is %q after the replacement-refusing converge, want 755", helperDir, got)
	}

	vm.ssh(t, "sudo chmod 700 "+recordsHelper)
	moved, err := bootstrap.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	refusing.VendorState = moved.VendorState
	got := refused(t, bootstrap.Apply(ctx, refusing, nil), refusal.CodeNotReady)
	if !strings.Contains(got.Message, recordsHelper) {
		t.Errorf("the refusal says %q, want it to name %s as what it would write over", got.Message, recordsHelper)
	}
	if got := mode(t, vm, recordsHelper); got != "700" {
		t.Errorf("%s is %q after the apply that refused it, want the refusal to have written nothing", recordsHelper, got)
	}

	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", VendorState: moved.VendorState}, nil); err != nil {
		t.Fatalf("the same apply accepting replacements = %v", err)
	}
	if got := mode(t, vm, recordsHelper); got != "755" {
		t.Errorf("%s is %q after the apply that accepted the write, want 755", recordsHelper, got)
	}
}

func TestLiveHealReassertsTheStateTierAndRefusesEverythingBesideWhole(t *testing.T) {
	vm := liveMachine(t)
	class := edge.ClassProduction
	p := bootstrapped(t, vm, class)
	ctx := context.Background()
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	healing := provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", Heal: true, RefuseReplacements: true}

	vm.ssh(t, "sudo chmod 700 "+recordsDir)
	if err := bootstrap.Apply(ctx, healing, nil); err != nil {
		t.Fatalf("heal over a drifted record tier = %v, want the state the deploy login owns reasserted", err)
	}
	if got := mode(t, vm, recordsDir); got != "750" {
		t.Errorf("%s is %q after a heal, want 750", recordsDir, got)
	}

	vm.ssh(t, "sudo chmod 700 "+recordsDir)
	vm.ssh(t, "sudo chmod 700 "+helperDir)
	got := refused(t, bootstrap.Apply(ctx, healing, nil), refusal.CodeDenied)
	if !strings.Contains(got.Message, helperDir) {
		t.Errorf("heal over a mixed set says %q, want it to name %s as what heal may not write", got.Message, helperDir)
	}
	if got := mode(t, vm, recordsDir); got != "700" {
		t.Errorf("%s is %q after a heal that refused, want a mixed set refused whole rather than half-done", recordsDir, got)
	}

	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("the apply that may write both = %v", err)
	}
	for path, want := range map[string]string{recordsDir: "750", helperDir: "755"} {
		if got := mode(t, vm, path); got != want {
			t.Errorf("%s is %q after the apply that may write it, want %q", path, got, want)
		}
	}
}

func TestLiveASymlinkWhereTheDeployLoginOwnsAPathIsRefusedRatherThanChowned(t *testing.T) {
	vm := liveMachine(t)
	class := edge.ClassProduction
	p := bootstrapped(t, vm, class)
	ctx := context.Background()
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}

	vm.sshAs(t, deployLogin, "rmdir "+recordsDir+" && ln -s /etc "+recordsDir)
	defer vm.ssh(t, "sudo rm -f "+recordsDir+" && sudo install -d -m 750 -o "+deployLogin+" -g "+deployLogin+" "+recordsDir)

	got := refused(t, bootstrap.Apply(ctx,
		provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", Heal: true, RefuseReplacements: true}, nil),
		refusal.CodeDenied)
	if !strings.Contains(got.Message, recordsDir) || !strings.Contains(got.Message, "/etc") {
		t.Errorf("heal over a path the deploy login pointed elsewhere says %q, want both the path and where it points named", got.Message)
	}
	if got := strings.TrimSpace(vm.ssh(t, "sudo stat -c %U /etc")); got != "root" {
		t.Fatalf("/etc is owned by %q after a heal that followed a binding into it, want root", got)
	}
}

func TestLiveHealAsTheDeployLoginReassertsItsOwnTierAndNothingBeside(t *testing.T) {
	vm := liveMachine(t)
	class := edge.ClassProduction
	bootstrapped(t, vm, class)

	bootstrap, err := vm.deploying(t).Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	healing := provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", Heal: true, RefuseReplacements: true}

	vm.sshAs(t, deployLogin, "chmod 700 "+recordsDir)
	if err := bootstrap.Apply(ctx, healing, nil); err != nil {
		t.Fatalf("heal as %s over its own drifted record tier = %v, want what that login owns reasserted without asking for root", deployLogin, err)
	}
	if got := mode(t, vm, recordsDir); got != "750" {
		t.Errorf("%s is %q after a heal driven by the login that owns it, want 750", recordsDir, got)
	}

	vm.sshAs(t, deployLogin, "chmod 700 "+recordsDir)
	vm.ssh(t, "sudo chmod 700 "+helperDir)
	defer vm.ssh(t, "sudo chmod 755 "+helperDir)
	got := refused(t, bootstrap.Apply(ctx, healing, nil), refusal.CodeDenied)
	if !strings.Contains(got.Message, helperDir) {
		t.Errorf("heal as %s over a mixed set says %q, want %s named as what that login may not write", deployLogin, got.Message, helperDir)
	}
	if got := mode(t, vm, recordsDir); got != "700" {
		t.Errorf("%s is %q after a heal that refused, want a mixed set refused whole rather than half-done", recordsDir, got)
	}
}
