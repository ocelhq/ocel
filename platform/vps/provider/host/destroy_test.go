package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type said struct{ lines []string }

func (s *said) Say(message string)    { s.lines = append(s.lines, message) }
func (s *said) Detail(message string) { s.lines = append(s.lines, message) }

func (s *said) Span(string, time.Time, time.Time, error, ...providerkit.Attr) {}

func (s *said) at(fragment string) int {
	return slices.IndexFunc(s.lines, func(line string) bool { return strings.Contains(line, fragment) })
}

func TestRemoveTakesWhatStandsAndSaysWhatItTookAndWhatItLeft(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := machine(map[providerkit.Class][]Item{class: bootstrapped(t, class)})
	report := &said{}

	if err := Bootstrap(stood.host()).Remove(context.Background(), class, report); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	for _, taken := range []string{
		"removed " + KindDir + " " + StateDir(class),
		"removed " + KindSealKey + " " + SealKeyPath(class),
		"removed " + KindUser + " " + deployUser,
		"removed " + KindDir + " " + ClassDir(class),
	} {
		if !slices.Contains(report.lines, taken) {
			t.Errorf("Remove() never said %q:\n%s", taken, strings.Join(report.lines, "\n"))
		}
	}
	if !slices.Contains(report.lines, "kept "+KindEngine+" "+dockerEngine) {
		t.Errorf("Remove() never says the engine stays:\n%s", strings.Join(report.lines, "\n"))
	}
	if report.at("ssh-keygen -R") < 0 {
		t.Errorf("Remove() never spells the line that drops this host from known_hosts:\n%s", strings.Join(report.lines, "\n"))
	}
	for _, command := range stood.taking() {
		if strings.Contains(command, quoted(dockerEngine)) || strings.Contains(command, quoted(dockerUnit)) {
			t.Errorf("Remove() ran %q, and removing ocel is not removing the workloads this host serves", command)
		}
	}
}

func TestRemoveTakesTheStampAfterEverythingBeneathIt(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := machine(map[providerkit.Class][]Item{class: bootstrapped(t, class)})

	if err := Bootstrap(stood.host()).Remove(context.Background(), class, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	stamp := stood.took(quoted(ClassDir(class)))
	if stamp < 0 {
		t.Fatalf("Remove() never took %s:\n%s", ClassDir(class), strings.Join(stood.taking(), "\n"))
	}
	for _, beneath := range []string{StateDir(class), SealKeyPath(class), SealHelper, deployUser} {
		if at := stood.took(quoted(beneath)); at < 0 || at > stamp {
			t.Errorf("Remove() took %s at command %d and the class directory at %d, and the stamp is what an interrupted destroy leaves behind",
				beneath, at, stamp)
		}
	}
}

func TestADestroyThatLandedIsNotReportedAsFailedBecauseTheConnectionWentAfterIt(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := machine(map[providerkit.Class][]Item{class: bootstrapped(t, class)})
	stood.after = func(b *bench, command string) {
		if !strings.HasSuffix(command, quoted(classRoot)) {
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.dead = errors.New("connection closed by remote host")
	}

	report := &said{}
	if err := Bootstrap(stood.host()).Remove(context.Background(), class, report); err != nil {
		t.Fatalf("Remove() = %v after every removal landed, and a host that is gone must not be reported as one that stayed", err)
	}
	if report.at("ssh-keygen -R") < 0 {
		t.Errorf("Remove() never spells the line that drops this host from known_hosts:\n%s", strings.Join(report.lines, "\n"))
	}
}

func TestAHostWhoseStampIsUnreadableCanStillBeDestroyed(t *testing.T) {
	t.Parallel()

	class, beside := providerkit.ClassProduction, providerkit.ClassPreview
	truncated := func(class providerkit.Class) Item {
		return Item{Kind: KindFile, Name: StampPath(class), Mode: 0o644, Owner: rootOwner, Content: []byte(`{"schema": 2, "sta`)}
	}
	stood := machine(map[providerkit.Class][]Item{
		class:  append(Items(class, []byte(aKey+"\n")), truncated(class)),
		beside: {truncated(beside)},
	})

	bootstrapper := Bootstrap(stood.host())
	if _, err := bootstrapper.PlanRemoval(context.Background(), class); err != nil {
		t.Fatalf("PlanRemoval() = %v over a host an apply left half-written, and no verb can clear it if destroy cannot read it", err)
	}
	if err := bootstrapper.Remove(context.Background(), class, nil); err != nil {
		t.Fatalf("Remove() = %v over a host an apply left half-written", err)
	}
	if stood.took(quoted(ClassDir(class))) < 0 {
		t.Errorf("Remove() left %s standing:\n%s", ClassDir(class), strings.Join(stood.taking(), "\n"))
	}
}

func TestTheHostRemovesNothingItCannotNameAsAPathItWrote(t *testing.T) {
	t.Parallel()

	stood := machine(nil)
	held := stood.host()
	for name, taken := range map[string]removal{
		"the engine every container on the host needs": keptEngine(),
		"the unit that starts it":                      taking(KindUnit, dockerUnit, ""),
		"a name rooted at nothing":                     taking(KindDir, dockerEngine, ""),
	} {
		err := held.remove(context.Background(), taken)
		var refusal providerkit.Refusal
		if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
			t.Errorf("Remove(%s) = %v, want a refusal: rm -rf is not the fallback for anything ocel was not asked about", name, err)
		}
	}
	if ran := stood.commands(); len(ran) != 0 {
		t.Errorf("Remove() ran %q over a host, and what ocel never wrote it never takes", ran)
	}
}

func TestADeployLoginSomethingStillHoldsDoesNotStrandTheDestroy(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := machine(map[providerkit.Class][]Item{class: bootstrapped(t, class)})
	stood.answer = func(command string) (session.Result, bool) {
		if !strings.HasPrefix(command, "userdel ") || strings.Contains(command, " -f ") {
			return session.Result{}, false
		}
		return session.Result{Code: 8, Stderr: "userdel: user " + deployUser + " is currently used by process 4021"}, true
	}

	if err := Bootstrap(stood.host()).Remove(context.Background(), class, nil); err != nil {
		t.Fatalf("Remove() = %v over a login a lingering session still holds, and every re-run would fail there again", err)
	}
	if stood.took(quoted(ClassDir(class))) < 0 {
		t.Errorf("Remove() stopped at the login and left %s standing:\n%s", ClassDir(class), strings.Join(stood.taking(), "\n"))
	}
}

func TestARootOtherClassesShareIsTakenOnlyWhileNothingElseIsUnderIt(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := machine(map[providerkit.Class][]Item{class: bootstrapped(t, class)})

	if err := Bootstrap(stood.host()).Remove(context.Background(), class, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := stood.taking()
	for _, shared := range []string{stateRoot, helperRoot, classRoot} {
		at := stood.took(quoted(shared))
		if at < 0 {
			t.Fatalf("Remove() left %s standing on a host that carries nothing else:\n%s", shared, strings.Join(taken, "\n"))
		}
		if !strings.HasPrefix(taken[at], "rmdir ") {
			t.Errorf("Remove() takes %s with %q: a class bootstrapped during the destroy loses its seal key to a survey drawn before it existed",
				shared, taken[at])
		}
	}
}

func TestTheLastDestroyLeavesNothingOcelEverWroteOnTheHost(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := machine(map[providerkit.Class][]Item{class: bootstrapped(t, class)})

	if err := Bootstrap(stood.host()).Remove(context.Background(), class, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := stood.taking()
	for _, item := range bootstrapped(t, class) {
		if item.Kind == KindEngine || item.Kind == KindUnit || gone(taken, item.Name) {
			continue
		}
		t.Errorf("%s stands after the last class on the host was destroyed:\n%s", item.ID(), strings.Join(taken, "\n"))
	}
}

func gone(taken []string, name string) bool {
	for _, command := range taken {
		if strings.HasSuffix(strings.TrimSuffix(command, " || true"), quoted(name)) {
			return true
		}
		if under, sweeping := strings.CutPrefix(command, "rm -rf "); sweeping &&
			strings.HasPrefix(name, strings.Trim(under, "'")+"/") {
			return true
		}
	}
	return false
}

func TestForgettingARecordOnAHostThatCarriesNoStoreIsAlreadyForgotten(t *testing.T) {
	t.Parallel()

	stood := machine(nil)
	name := providerkit.RecordName{providerkit.RootConformance, string(providerkit.ClassProduction), t.Name()}
	if err := providerkit.Forget(context.Background(), NewRecords(stood.host()), name); err != nil {
		t.Fatalf("Forget() over a host a destroy has cleared = %v, want cleanup that does not need the store back", err)
	}
	for _, command := range stood.commands() {
		if strings.HasPrefix(command, quoted(recordsHelper)+" ") {
			t.Errorf("Forget() ran %q against a host that carries no helper at all", command)
		}
	}
}

func TestPlanRemovalNamesTheGroupAfterTheMachineItRunsOn(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := machine(map[providerkit.Class][]Item{class: bootstrapped(t, class)})

	plan, err := Bootstrap(stood.host()).PlanRemoval(context.Background(), class)
	if err != nil {
		t.Fatalf("PlanRemoval() = %v", err)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("PlanRemoval() carries %d groups, want the one machine being destroyed", len(plan.Groups))
	}
	group := plan.Groups[0]
	if want := "vps/ada@ocelbox"; group.Name != want {
		t.Errorf("PlanRemoval() named the group %q, want %q", group.Name, want)
	}
	if group.Action != providerkit.ActionDelete {
		t.Errorf("PlanRemoval() plans the group as %q, want a delete", group.Action)
	}
	for _, bearing := range []string{StateDir(class), SealKeyPath(class)} {
		at := slices.IndexFunc(group.Changes, func(c providerkit.Change) bool { return c.Name == bearing })
		if at < 0 {
			t.Fatalf("PlanRemoval() never plans %s", bearing)
		}
		if group.Changes[at].Reason == "" {
			t.Errorf("PlanRemoval() takes %s with no reason, and the typed confirmation must name what is unrecoverable before a user types", bearing)
		}
	}
}

func TestPlanRemovalOfAHostCarryingNothingPlansNothing(t *testing.T) {
	t.Parallel()

	stood := machine(nil)
	plan, err := Bootstrap(stood.host()).PlanRemoval(context.Background(), providerkit.ClassProduction)
	if err != nil {
		t.Fatalf("PlanRemoval() = %v", err)
	}
	if len(plan.Groups) != 0 {
		t.Errorf("PlanRemoval() over a machine with no ocel on it plans %d groups, want nothing to destroy", len(plan.Groups))
	}
}
