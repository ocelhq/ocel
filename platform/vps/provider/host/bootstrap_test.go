package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func standingHost() Reading {
	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	return Reading{
		Class:    class,
		Present:  true,
		Keys:     keys,
		Stamp:    Stamp{Schema: providerkit.BootstrapSchema, State: StateComplete, Digests: digests(Items(class, keys))},
		Observed: digests(Items(class, keys)),
	}
}

func drifted(t *testing.T, read Reading, name string) Reading {
	t.Helper()

	for _, item := range Items(read.Class, read.Keys) {
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
	work, err := healable(read)
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
	work, err := healable(read)
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
		ClassDir(class), SealKeyPath(class), SealHelper, sudoersSeal, deployUser, dockerEngine, dockerUnit,
		sshDir, authorizedKeys,
	} {
		read := drifted(t, standingHost(), name)
		refused := refusal(t, second(healable(read)), providerkit.CodeDenied)
		if !strings.Contains(refused.Message, name) {
			t.Errorf("heal over a drifted %s says %q, want it named as what heal may not write", name, refused.Message)
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
	if _, err := healing(read, true); err != nil {
		t.Errorf("heal driven by a login that cannot read the key's bytes = %v, want the key that stands taken as the key that stands: nothing but root ever writes it", err)
	}
}

func TestHealRunsTheUnattendedGateTheRestOfApplyRuns(t *testing.T) {
	t.Parallel()

	read := drifted(t, standingHost(), RecordsDir(providerkit.ClassProduction))
	work, err := healing(read, true)
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
	for _, item := range Items(class, read.Keys) {
		if item.Name == sudoersSeal || item.Kind == KindUser {
			delete(read.Observed, item.ID())
		}
	}
	work, err := healing(read, true)
	if err != nil {
		t.Fatalf("heal over a survey that could read neither %s nor the shadow database = %v, want the record tier reasserted anyway", sudoersSeal, err)
	}
	if len(work) != 1 || work[0].Name != RecordsDir(class) {
		t.Errorf("healing() = %v, want only %s", ids(work), RecordsDir(class))
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
	fresh := Reading{Class: class, Observed: map[string]string{}}
	refusal(t, second(healable(fresh)), providerkit.CodeDenied)
}

func second(_ []Item, err error) error { return err }

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
	fresh := Reading{Class: class, Observed: map[string]string{}}
	if err := refuseReplacements(fresh, Items(class, nil)); err != nil {
		t.Errorf("an unattended apply over a machine nothing has bootstrapped = %v, want the installs to proceed", err)
	}
}

func TestAnUnattendedApplyWillNotWriteOverWhatAlreadyStands(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	for _, name := range []string{recordsHelper, SealKeyPath(class), deployUser, dockerEngine} {
		read := drifted(t, standingHost(), name)
		refused := refusal(t, refuseReplacements(read, Items(read.Class, read.Keys)), providerkit.CodeNotReady)
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
		if err := refuseReplacements(read, Items(read.Class, read.Keys)); err != nil {
			t.Errorf("an unattended apply over a host whose %s has moved = %v, want a converge that destroys nothing to proceed", name, err)
		}
	}
}

func TestNothingHealMayWriteIsAReplacementClassChange(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	for _, item := range Items(class, []byte(aKey+"\n")) {
		if deployOwned(item) && replacing(item) {
			t.Errorf("heal may write %s and writing it replaces rather than converges, so the one unattended path with nobody watching would rebuild it", item.ID())
		}
	}
}
