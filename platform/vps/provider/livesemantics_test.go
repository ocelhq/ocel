//go:build integration

package vps_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
)

const (
	helperDir    = "/usr/local/lib/ocel"
	keyValuesDir = "/var/lib/ocel/production/keyvalues"
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

func (k *killer) Warn(string) {}

func (k *killer) Error(string) {}

func (k *killer) Detail(string) {}

func (k *killer) Debug(string) {}

func (k *killer) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

type sayings []string

func (s *sayings) Say(message string) { *s = append(*s, message) }

func (s *sayings) Warn(message string) { *s = append(*s, message) }

func (s *sayings) Error(message string) { *s = append(*s, message) }

func (s *sayings) Detail(string) {}

func (s *sayings) Debug(line string) { *s = append(*s, line) }

func (s *sayings) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

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

func mode(t *testing.T, vm liveBox, path string) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t, "sudo stat -c %a "+path))
}

type planning interface {
	Plan(context.Context, provider.BootstrapRequest) (provider.Plan, error)
}

func stillMoving(t *testing.T, planner planning, tier environment.Tier) string {
	t.Helper()

	plan, err := planner.Plan(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"})
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
	tier := environment.TierProduction
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := bootstrap.Remove(ctx, tier, progress.Discard()); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	dying, kill := context.WithCancel(ctx)
	defer kill()
	err = bootstrap.Apply(dying, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"},
		&killer{at: "Installed file " + boxapi.LiveBinary, kill: kill})
	if err == nil {
		t.Fatal("the apply ran to completion, and a half-applied host is what this proves recovery from")
	}

	if stamp := stampOn(t, vm); stamp.State != machine.StateApplying {
		t.Fatalf("the stamp reads state %q after an apply that died, want %q", stamp.State, machine.StateApplying)
	}
	described, err := bootstrap.Describe(ctx, tier)
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

	plan, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"})
	if err != nil {
		t.Fatalf("Plan() over a half-applied host = %v", err)
	}
	group := onlyGroup(t, plan)
	if group.Action != provider.ActionUpdate {
		t.Errorf("Plan() over a half-applied host plans %q, want %q", group.Action, provider.ActionUpdate)
	}
	for _, name := range []string{helperDir, boxapi.LiveBinary} {
		if planned := planFor(group, name); planned.Action != provider.ActionKeep {
			t.Errorf("Plan() shows %s as %q, want the work the dead apply already did left alone", name, planned.Action)
		}
	}
	for _, name := range []string{deployLogin, "/var/lib/ocel/production", keyValuesDir} {
		if planned := planFor(group, name); planned.Action != provider.ActionCreate {
			t.Errorf("Plan() shows %s as %q, want the work that is left", name, planned.Action)
		}
	}

	var said sayings
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, &said); err != nil {
		t.Fatalf("the same command over a half-applied host = %v, want recovery to be the first run's command", err)
	}
	unchanged := "File " + boxapi.LiveBinary + " is already current"
	if !slices.Contains(said, unchanged) {
		t.Errorf("the apply said %q, want %q: what the plan showed as a no-op is declared rather than passed over", said, unchanged)
	}
	if stamp := stampOn(t, vm); stamp.State != machine.StateComplete {
		t.Errorf("the stamp reads state %q after the run that finished it, want %q", stamp.State, machine.StateComplete)
	}
	finished, err := bootstrap.Describe(ctx, tier)
	if err != nil {
		t.Fatal(err)
	}
	if !finished.Stacks[0].DigestCurrent {
		t.Errorf("Describe() still reads the host as drifted after the apply that finished it, %s",
			stillMoving(t, bootstrap, tier))
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
	tier := environment.TierProduction
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, progress.Discard()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	defer func() {
		if err := bootstrap.Remove(ctx, tier, progress.Discard()); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	vm.purges(t)
	vm.ssh(t, "sudo rm -f /etc/sudoers.d/ocel-seal-*")
	vm.forgetsTheDeployLogin(t)
	refusing := provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite", RefuseReplacements: true}
	if err := bootstrap.Apply(ctx, refusing, progress.Discard()); err != nil {
		t.Fatalf("an apply that refuses replacements over a machine with none of ocel's own state = %v, want absent-to-present to proceed", err)
	}

	vm.ssh(t, "sudo chmod 700 "+helperDir)
	if _, err := bootstrap.Describe(ctx, tier); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, refusing, progress.Discard()); err != nil {
		t.Fatalf("an apply that refuses replacements over a host whose %s has moved mode = %v, want a converge that destroys nothing to proceed", helperDir, err)
	}
	if got := mode(t, vm, helperDir); got != "755" {
		t.Errorf("%s is %q after the replacement-refusing converge, want 755", helperDir, got)
	}

	vm.ssh(t, "sudo chmod 700 "+boxapi.ReleasesHelper)
	if _, err := bootstrap.Describe(ctx, tier); err != nil {
		t.Fatal(err)
	}
	got := refused(t, bootstrap.Apply(ctx, refusing, progress.Discard()), refusal.CodeNotReady)
	if !strings.Contains(got.Message, boxapi.ReleasesHelper) {
		t.Errorf("the refusal says %q, want it to name %s as what it would write over", got.Message, boxapi.ReleasesHelper)
	}
	if got := mode(t, vm, boxapi.ReleasesHelper); got != "700" {
		t.Errorf("%s is %q after the apply that refused it, want the refusal to have written nothing", boxapi.ReleasesHelper, got)
	}

	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, progress.Discard()); err != nil {
		t.Fatalf("the same apply accepting replacements = %v", err)
	}
	if got := mode(t, vm, boxapi.ReleasesHelper); got != "755" {
		t.Errorf("%s is %q after the apply that accepted the write, want 755", boxapi.ReleasesHelper, got)
	}
}
