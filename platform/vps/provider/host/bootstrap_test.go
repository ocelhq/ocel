package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func standingHost() Reading {
	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	return Reading{
		Arch:     ArchAMD64,
		Class:    class,
		Present:  true,
		Keys:     keys,
		Stamp:    Stamp{Schema: providerkit.BootstrapSchema, State: StateComplete, Digests: digests(Items(class, keys, ArchAMD64))},
		Observed: digests(Items(class, keys, ArchAMD64)),
	}
}

func drifted(t *testing.T, read Reading, name string) Reading {
	t.Helper()

	for _, item := range Items(read.Class, read.Keys, ArchAMD64) {
		if item.Name != name {
			continue
		}
		read.Observed[item.ID()] = "a digest nothing this ocel writes produces"
		return read
	}
	t.Fatalf("nothing in the item set is named %s", name)
	return read
}

func refusal(t *testing.T, err error, code providerkit.Code) providerkit.Refusal {
	t.Helper()

	var refused providerkit.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("error = %v, want a refusal the CLI can render", err)
	}
	if refused.Code != code {
		t.Fatalf("refusal code = %q, want %q", refused.Code, code)
	}
	return refused
}

func TestHealReassertsTheStateTheDeployLoginOwns(t *testing.T) {
	t.Parallel()

	read := drifted(t, standingHost(), RecordsDir(providerkit.ClassProduction))
	work, _, err := healable(read)
	if err != nil {
		t.Fatalf("healable() over a drifted record tier = %v, want the deploy login's own state reasserted", err)
	}
	if len(work) != 1 || work[0].Name != RecordsDir(providerkit.ClassProduction) {
		t.Fatalf("healable() = %v, want only %s", ids(work), RecordsDir(providerkit.ClassProduction))
	}
}

func TestHealRefusesAMixedSetWholeRatherThanDoingThePartItMay(t *testing.T) {
	t.Parallel()

	read := drifted(t, drifted(t, standingHost(), RecordsDir(providerkit.ClassProduction)), recordsHelper)
	work, _, err := healable(read)
	refused := refusal(t, err, providerkit.CodeDenied)
	if !strings.Contains(refused.Message, recordsHelper) {
		t.Errorf("the refusal says %q, want it to name %s as what heal may not write", refused.Message, recordsHelper)
	}
	if len(work) != 0 {
		t.Errorf("healable() = %v alongside its refusal, want a mixed set refused whole", ids(work))
	}
}

func TestHealRefusesEveryItemOutsideTheRecordTier(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	for _, name := range []string{
		ClassDir(class), SealKeyPath(class), SealHelper, sudoersSeal, deployUser, sshDir, authorizedKeys,
	} {
		read := drifted(t, standingHost(), name)
		refused := refusal(t, second(healable(read)), providerkit.CodeDenied)
		if !strings.Contains(refused.Message, name) {
			t.Errorf("heal over a drifted %s says %q, want it named as what heal may not write", name, refused.Message)
		}
	}
}

func TestHealLeavesWhatADaemonHoldsRatherThanRefusingOverIt(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	held := []string{dockerEngine, dockerUnit, ProxyNetwork, ProxyContainer}
	for _, name := range held {
		read := drifted(t, drifted(t, standingHost(), RecordsDir(class)), name)
		work, left, err := healing(read, true)
		if err != nil {
			t.Fatalf("heal over a box whose %s is not as the stamp records = %v, want it left: heal stands nothing up and a daemon reports what it holds, so a stopped one is not drift heal can refuse over",
				name, err)
		}
		if len(work) != 1 || work[0].Name != RecordsDir(class) {
			t.Errorf("healing() over a drifted %s = %v, want only the record tier", name, ids(work))
		}
		if !slices.ContainsFunc(left, func(id string) bool { return strings.HasSuffix(id, " "+name) }) {
			t.Errorf("heal left %v over a drifted %s, and a box told nothing about what heal declined is one nobody can read the exit code of", left, name)
		}
	}

	for _, item := range Items(class, []byte(aKey+"\n"), ArchAMD64) {
		if daemonHeld(item) && deployOwned(item) {
			t.Errorf("%s is both a daemon's to report and heal's to write, and the two dispositions cannot both hold", item.ID())
		}
	}
}

func TestHealWillNotReassertRecordsSealedToAKeyThatIsGone(t *testing.T) {
	t.Parallel()

	read := drifted(t, standingHost(), RecordsDir(providerkit.ClassProduction))
	read.Stamp.Seal = Seal{Fingerprint: "the key every value this class holds was sealed to"}
	delete(read.Observed, sealKey(read.Class).ID())
	refused := refusal(t, second(healing(read, true)), providerkit.CodeInvalid)
	if !strings.Contains(refused.Message, SealKeyPath(read.Class)) {
		t.Errorf("heal over a class whose seal key vanished says %q, want it to name the key: reasserting the records and calling the bootstrap refreshed loses the failure until unseal time", refused.Message)
	}
}

func TestHealReadsAKeyItCannotOpenAsTheKeyThatStands(t *testing.T) {
	t.Parallel()

	read := drifted(t, standingHost(), RecordsDir(providerkit.ClassProduction))
	read.Stamp.Seal = Seal{Fingerprint: "the key the stamp records"}
	read.Seal = Seal{}
	if _, _, err := healing(read, true); err != nil {
		t.Errorf("heal driven by a login that cannot read the key's bytes = %v, want the key that stands taken as the key that stands: nothing but root ever writes it", err)
	}
}

func TestHealRunsTheUnattendedGateTheRestOfApplyRuns(t *testing.T) {
	t.Parallel()

	read := drifted(t, standingHost(), RecordsDir(providerkit.ClassProduction))
	work, _, err := healing(read, true)
	if err != nil {
		t.Fatalf("an unattended heal over a drifted record tier = %v, want the converge to proceed", err)
	}
	if err := refuseReplacements(read, work); err != nil {
		t.Errorf("the gate over what an unattended heal admitted = %v, want heal to have refused it before writing a byte", err)
	}
}

func TestHealIsNotWedgedByWhatItsOwnLoginCannotSee(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	read := drifted(t, standingHost(), RecordsDir(class))
	var unread []string
	for _, item := range Items(class, read.Keys, ArchAMD64) {
		hidden := item.Kind == KindFile && item.Owner == rootOwner && item.Mode&0o004 == 0
		if !hidden && item.Kind != KindUser {
			continue
		}
		unread = append(unread, item.ID())
		delete(read.Observed, item.ID())
	}
	for _, hidden := range []string{KindFile + " " + sudoersSeal, KindFile + " " + ProxyHelper} {
		if !slices.Contains(unread, hidden) {
			t.Fatalf("%s reads as one %s can hash, and a survey drawn by that login reports nothing for it: %v", hidden, deployUser, unread)
		}
	}
	work, _, err := healing(read, true)
	if err != nil {
		t.Fatalf("heal over a survey that could read none of %v = %v, want the record tier reasserted anyway", unread, err)
	}
	if len(work) != 1 || work[0].Name != RecordsDir(class) {
		t.Errorf("healing() = %v, want only %s", ids(work), RecordsDir(class))
	}
}

func TestHealAsALoginThatIsNeitherRootNorSudoAsksForNeither(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stands := bootstrapped(t, class)
	records := RecordsDir(class)
	for at, item := range stands {
		if item.Kind == KindDir && item.Name == records {
			stands[at].Mode = 0o700
		}
	}
	stood := machine(map[providerkit.Class][]Item{class: stands})
	stood.facts = session.Facts{Arch: "x86_64"}
	stood.floor = providerkit.Refuse(providerkit.CodeDenied,
		"ada@ocelbox can neither act as root nor run sudo without a password, and bootstrap writes as root throughout")
	stood.answer = func(command string) (session.Result, bool) {
		if command != "cat ~/.ssh/authorized_keys 2>/dev/null" {
			return session.Result{}, false
		}
		return session.Result{Stdout: aKey + "\n"}, true
	}

	err := Bootstrap(stood.host()).Apply(context.Background(),
		providerkit.BootstrapRequest{Class: class, Writer: "the-suite", Heal: true, Unattended: true}, nil)
	if err != nil {
		t.Fatalf("heal driven by the deploy login = %v, want what that login owns reasserted without asking for root", err)
	}
	ran := stood.commands()
	if !slices.ContainsFunc(ran, func(command string) bool {
		return strings.HasPrefix(command, "install -d ") && strings.HasSuffix(command, quoted(records))
	}) {
		t.Errorf("heal never wrote %s back:\n%s", records, strings.Join(ran, "\n"))
	}
	for _, command := range ran {
		if strings.HasPrefix(command, "sudo ") {
			t.Errorf("heal ran %q, and the login it runs as holds no sudo at all", command)
		}
	}
}

func TestHealNeverFinishesAnApplyThatDiedMidWay(t *testing.T) {
	t.Parallel()

	read := drifted(t, standingHost(), RecordsDir(providerkit.ClassProduction))
	read.Stamp.State = StateApplying
	refused := refusal(t, second(healable(read)), providerkit.CodeDenied)
	if !strings.Contains(refused.Message, StampPath(read.Class)) {
		t.Errorf("heal over an unfinished apply says %q, want it to name the stamp that says so", refused.Message)
	}
}

func TestHealHasNothingToReassertWhereNoBootstrapStands(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	fresh := Reading{Arch: ArchAMD64, Class: class, Observed: map[string]string{}}
	refusal(t, second(healable(fresh)), providerkit.CodeDenied)
}

func second(_ []Item, _ []string, err error) error { return err }

func ids(items []Item) []string {
	var out []string
	for _, item := range items {
		out = append(out, item.ID())
	}
	return out
}

func TestAnUnattendedApplyInstallsWhatIsAbsent(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	fresh := Reading{Arch: ArchAMD64, Class: class, Observed: map[string]string{}}
	if err := refuseReplacements(fresh, Items(class, nil, ArchAMD64)); err != nil {
		t.Errorf("an unattended apply over a machine nothing has bootstrapped = %v, want the installs to proceed", err)
	}
}

func TestAnUnattendedApplyWillNotWriteOverWhatAlreadyStands(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	for _, name := range []string{recordsHelper, SealKeyPath(class), deployUser, dockerEngine} {
		read := drifted(t, standingHost(), name)
		refused := refusal(t, refuseReplacements(read, Items(read.Class, read.Keys, ArchAMD64)), providerkit.CodeNotReady)
		if !strings.Contains(refused.Message, name) {
			t.Errorf("the refusal says %q, want it to name %s as the thing it would write over", refused.Message, name)
		}
		if !strings.Contains(refused.Message, "--yes") {
			t.Errorf("the refusal says %q, want it to name the flag that accepts the write", refused.Message)
		}
	}
}

func TestAnUnattendedApplyConvergesAHostRatherThanRefusingEveryChange(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	for _, name := range []string{dockerUnit, RecordsDir(class), stateRoot, helperRoot} {
		read := drifted(t, standingHost(), name)
		if err := refuseReplacements(read, Items(read.Class, read.Keys, ArchAMD64)); err != nil {
			t.Errorf("an unattended apply over a host whose %s has moved = %v, want a converge that destroys nothing to proceed", name, err)
		}
	}
}

func TestNothingHealMayWriteIsAReplacementClassChange(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	for _, item := range Items(class, []byte(aKey+"\n"), ArchAMD64) {
		if deployOwned(item) && replacing(item) {
			t.Errorf("heal may write %s and writing it replaces rather than converges, so the one unattended path with nobody watching would rebuild it", item.ID())
		}
	}
}
