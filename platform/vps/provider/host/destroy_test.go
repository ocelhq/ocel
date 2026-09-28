package host

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
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type said struct{ lines []string }

func (s *said) Say(message string)  { s.lines = append(s.lines, message) }
func (s *said) Warn(message string) { s.lines = append(s.lines, message) }

func (s *said) Error(message string)  { s.lines = append(s.lines, message) }
func (s *said) Detail(message string) { s.lines = append(s.lines, message) }
func (s *said) Debug(line string)     { s.lines = append(s.lines, line) }

func (s *said) Span(string, time.Time, time.Time, error, ...progress.Attr) {}

func (s *said) at(fragment string) int {
	return slices.IndexFunc(s.lines, func(line string) bool { return strings.Contains(line, fragment) })
}

func TestRemoveTakesWhatIsInstalledAndSaysWhatItTookAndWhatItLeft(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})
	progress := &said{}

	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), tier, progress); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	for _, taken := range []string{
		"Removed directory " + StateDir(tier),
		"Removed seal key " + SealKeyPath(tier),
		"Removed user " + deployUser,
		"Removed directory " + TierDir(tier),
	} {
		if !slices.Contains(progress.lines, taken) {
			t.Errorf("Remove() never said %q:\n%s", taken, strings.Join(progress.lines, "\n"))
		}
	}
	if !slices.Contains(progress.lines, "Kept the Docker engine: docker and its containers stay") {
		t.Errorf("Remove() never says the engine stays:\n%s", strings.Join(progress.lines, "\n"))
	}
	if progress.at("ssh-keygen -R") < 0 {
		t.Errorf("Remove() never spells the line that drops this host from known_hosts:\n%s", strings.Join(progress.lines, "\n"))
	}
	for _, command := range box.taking() {
		if strings.Contains(command, quoted(dockerEngine)) || strings.Contains(command, quoted(dockerUnit)) {
			t.Errorf("Remove() ran %q, and removing ocel is not removing the workloads this host serves", command)
		}
	}
}

func TestRemoveTakesTheStampAfterEverythingBeneathIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})

	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), tier, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	stamp := box.took(quoted(TierDir(tier)))
	if stamp < 0 {
		t.Fatalf("Remove() never took %s:\n%s", TierDir(tier), strings.Join(box.taking(), "\n"))
	}
	for _, beneath := range []string{StateDir(tier), SealKeyPath(tier), boxstore.SealHelper, deployUser} {
		if at := box.took(quoted(beneath)); at < 0 || at > stamp {
			t.Errorf("Remove() took %s at command %d and the tier directory at %d, and the stamp is what an interrupted destroy leaves behind",
				beneath, at, stamp)
		}
	}
}

func TestADestroyThatLandedIsNotReportedAsFailedBecauseTheConnectionWentAfterIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})
	box.after = func(b *bench, command string) {
		if !strings.HasSuffix(command, quoted(tierRoot)) {
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.dead = errors.New("connection closed by remote host")
	}

	progress := &said{}
	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), tier, progress); err != nil {
		t.Fatalf("Remove() = %v after every removal landed, and a host that is gone must not be reported as one that stayed", err)
	}
	if progress.at("ssh-keygen -R") < 0 {
		t.Errorf("Remove() never spells the line that drops this host from known_hosts:\n%s", strings.Join(progress.lines, "\n"))
	}
}

func TestAHostWhoseStampIsUnreadableCanStillBeDestroyed(t *testing.T) {
	t.Parallel()

	tier, beside := environment.TierProduction, environment.TierPreview
	truncated := func(tier environment.Tier) Item {
		return Item{Kind: KindFile, Name: StampPath(tier), Mode: 0o644, Owner: rootOwner, Content: []byte(`{"schema": 2, "sta`)}
	}
	box := machine(map[environment.Tier][]Item{
		tier:   append(Items(tier, []byte(aKey+"\n"), ArchAMD64, Front{}), truncated(tier)),
		beside: {truncated(beside)},
	})

	bootstrap := NewBootstrap(box.host(), testVendor, "shop")
	if _, err := bootstrap.PlanRemove(context.Background(), tier); err != nil {
		t.Fatalf("PlanRemove() = %v over a host an apply left half-written, and no verb can clear it if destroy cannot read it", err)
	}
	if err := bootstrap.Remove(context.Background(), tier, nil); err != nil {
		t.Fatalf("Remove() = %v over a host an apply left half-written", err)
	}
	if box.took(quoted(TierDir(tier))) < 0 {
		t.Errorf("Remove() left %s in place:\n%s", TierDir(tier), strings.Join(box.taking(), "\n"))
	}
}

func TestTheHostRemovesNothingItCannotNameAsAPathItWrote(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	h := box.host()
	for name, taken := range map[string]removal{
		"the engine every container on the host needs": keptEngine(),
		"the unit that starts it":                      taking(KindUnit, dockerUnit, ""),
		"a name rooted at nothing":                     taking(KindDir, dockerEngine, ""),
	} {
		_, err := h.remove(context.Background(), taken)
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Errorf("Remove(%s) = %v, want a refusal: rm -rf is not the fallback for anything ocel was not asked about", name, err)
		}
	}
	if ran := box.commands(); len(ran) != 0 {
		t.Errorf("Remove() ran %q over a host, and what ocel never wrote it never takes", ran)
	}
}

func TestADeployLoginSomethingStillUsesDoesNotStrandTheDestroy(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})
	box.answer = func(command string) (session.Result, bool) {
		if !strings.HasPrefix(command, "userdel ") || strings.Contains(command, " -f ") {
			return session.Result{}, false
		}
		return session.Result{Code: 8, Stderr: "userdel: user " + deployUser + " is currently used by process 4021"}, true
	}

	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), tier, nil); err != nil {
		t.Fatalf("Remove() = %v over a login a lingering session still uses, and every re-run would fail there again", err)
	}
	if box.took(quoted(TierDir(tier))) < 0 {
		t.Errorf("Remove() stopped at the login and left %s in place:\n%s", TierDir(tier), strings.Join(box.taking(), "\n"))
	}
}

func TestARootOtherTiersShareIsTakenOnlyWhileNothingElseIsUnderIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})

	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), tier, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := box.taking()
	for _, shared := range []string{stateRoot, boxstore.Dir, tierRoot} {
		at := slices.IndexFunc(taken, func(command string) bool {
			return strings.Contains(command, quoted(shared)) && !strings.HasPrefix(command, routingLocked("-x"))
		})
		if at < 0 {
			t.Fatalf("Remove() left %s in place on a host that has nothing else:\n%s", shared, strings.Join(taken, "\n"))
		}
		if !strings.HasPrefix(taken[at], "rmdir ") {
			t.Errorf("Remove() takes %s with %q: a tier bootstrapped during the destroy loses its seal key to a survey drawn before it existed",
				shared, taken[at])
		}
	}
}

func TestTheLastDestroyLeavesNothingOcelEverWroteOnTheHost(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})

	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), tier, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := box.taking()
	for _, item := range bootstrapped(t, tier) {
		if item.Kind == KindEngine || item.Kind == KindUnit || gone(taken, item.Name) {
			continue
		}
		t.Errorf("%s remains after the last tier on the host was destroyed:\n%s", item.ID(), strings.Join(taken, "\n"))
	}
}

func TestTheLastDestroyTakesTheLockDeploysLoadImagesUnderBeforeTheStateRoot(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	loaded := Item{Kind: KindFile, Name: imagesLock, Mode: 0o644, Owner: stateOwner}
	box := machine(map[environment.Tier][]Item{tier: append(bootstrapped(t, tier), loaded)})

	if err := NewBootstrap(box.host(), testVendor, "shop").Remove(context.Background(), tier, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	taken := box.taking()
	lock := slices.IndexFunc(taken, func(command string) bool { return strings.HasSuffix(command, quoted(imagesLock)) })
	if lock < 0 {
		t.Fatalf("Remove() left %s, which every deploy's image load creates, so %s outlives the last tier:\n%s",
			imagesLock, stateRoot, strings.Join(taken, "\n"))
	}
	root := slices.IndexFunc(taken, func(command string) bool { return strings.HasPrefix(command, "rmdir "+quoted(stateRoot)+" ") })
	if root < lock {
		t.Errorf("Remove() tried %s at command %d, before the lock inside it at %d", stateRoot, root, lock)
	}
}

func gone(taken []string, name string) bool {
	for _, command := range taken {
		if strings.HasSuffix(strings.TrimSuffix(command, " || true"), quoted(name)) {
			return true
		}
		if strings.Contains(command, "docker network rm "+quoted(name)) {
			return true
		}
		if strings.HasPrefix(command, "rmdir "+quoted(name)+" ") {
			return true
		}
		if under, sweeping := strings.CutPrefix(command, "rm -rf "); sweeping &&
			strings.HasPrefix(name, strings.Trim(under, "'")+"/") {
			return true
		}
	}
	return false
}

func TestForgettingARecordOnAHostThatHasNoStoreIsAlreadyForgotten(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	name := records.Name{records.RootConformance, string(environment.TierProduction), t.Name()}
	if err := records.Forget(context.Background(), NewRecords(box.host()), name); err != nil {
		t.Fatalf("Forget() over a host a destroy has cleared = %v, want cleanup that does not need the store back", err)
	}
	for _, command := range box.commands() {
		if strings.HasPrefix(command, quoted(boxstore.RecordsHelper)+" ") {
			t.Errorf("Forget() ran %q against a host that has no helper at all", command)
		}
	}
}

func TestPlanRemovalNamesTheGroupAfterTheMachineItRunsOn(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := machine(map[environment.Tier][]Item{tier: bootstrapped(t, tier)})

	plan, err := NewBootstrap(box.host(), testVendor, "shop").PlanRemove(context.Background(), tier)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("PlanRemove() has %d groups, want the one machine being destroyed", len(plan.Groups))
	}
	group := plan.Groups[0]
	if want := "vps/ada@ocelbox"; group.Name != want {
		t.Errorf("PlanRemove() named the group %q, want %q", group.Name, want)
	}
	if group.Action != provider.ActionDelete {
		t.Errorf("PlanRemove() plans the group as %q, want a delete", group.Action)
	}
	for _, bearing := range []string{StateDir(tier), SealKeyPath(tier)} {
		at := slices.IndexFunc(group.Changes, func(c provider.Change) bool { return c.Name == bearing })
		if at < 0 {
			t.Fatalf("PlanRemove() never plans %s", bearing)
		}
		if group.Changes[at].Reason == "" {
			t.Errorf("PlanRemove() takes %s with no reason, and the typed confirmation must name what is unrecoverable before a user types", bearing)
		}
	}
}

func TestEverySingletonIsNamedByThePlanThatTakesTheLastTierAndByNoOther(t *testing.T) {
	t.Parallel()

	production, preview := environment.TierProduction, environment.TierPreview
	keys := []byte(aKey + "\n")
	current := Reading{Arch: ArchAMD64, Tier: production, Keys: keys, Observed: digests(Items(production, keys, ArchAMD64, Front{}))}
	beside := Reading{Arch: ArchAMD64, Tier: preview, Keys: keys, Observed: digests(Items(preview, keys, ArchAMD64, Front{}))}
	singletons := []string{
		stateRoot, boxstore.Dir, boxstore.RecordsHelper, boxstore.SealHelper, SwitchboardBinary, ProxyConfig, live.RoutingTable, sshDir, tierRoot, deployUser,
	}

	for _, singleton := range singletons {
		if kept := removalOf(removing(current, beside, appsPresent{}), singleton); kept.action == provider.ActionDelete {
			t.Errorf("destroying one tier takes %s, and the sibling tier still installed on this host deploys through it", singleton)
		}
	}
	last := removing(current, Reading{Arch: ArchAMD64, Tier: preview, Observed: map[string]string{}}, appsPresent{})
	for _, singleton := range singletons {
		if gone := removalOf(last, singleton); gone.action != provider.ActionDelete {
			t.Errorf("destroying the last tier plans %s as %q, and a singleton nothing uses is one nobody revokes", singleton, gone.action)
		}
	}
}

func TestPlanRemovalOfAHostWithNothingPlansNothing(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	plan, err := NewBootstrap(box.host(), testVendor, "shop").PlanRemove(context.Background(), environment.TierProduction)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	if len(plan.Groups) != 0 {
		t.Errorf("PlanRemove() over a machine with no ocel on it plans %d groups, want nothing to destroy", len(plan.Groups))
	}
}
