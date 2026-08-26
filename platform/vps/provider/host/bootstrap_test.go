package host

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

func standingHost(t *testing.T) Reading {
	t.Helper()

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

	read := drifted(t, standingHost(t), RecordsDir(providerkit.ClassProduction))
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

	read := drifted(t, drifted(t, standingHost(t), RecordsDir(providerkit.ClassProduction)), recordsHelper)
	work, err := healable(read)
	refused := refusal(t, err, providerkit.CodeDenied)
	if !strings.Contains(refused.Message, recordsHelper) {
		t.Errorf("the refusal says %q, want it to name %s as what heal may not write", refused.Message, recordsHelper)
	}
	if len(work) != 0 {
		t.Errorf("healable() = %v alongside its refusal, want a mixed set refused whole", ids(work))
	}
}

func TestHealWritesNothingOutsideTheStateDirectoryUnderAnyIdentity(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	for _, name := range []string{
		ClassDir(class), SealKeyPath(class), SealHelper, sudoersSeal, deployUser, dockerEngine, dockerUnit,
	} {
		read := drifted(t, standingHost(t), name)
		refused := refusal(t, second(healable(read)), providerkit.CodeDenied)
		if !strings.Contains(refused.Message, name) {
			t.Errorf("heal over a drifted %s says %q, want it named as what heal may not write", name, refused.Message)
		}
	}
}

func TestHealNeverFinishesAnApplyThatDiedMidWay(t *testing.T) {
	t.Parallel()

	read := drifted(t, standingHost(t), RecordsDir(providerkit.ClassProduction))
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

func second(work []Item, err error) error { return err }

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
	if err := admitReplacements(fresh, Items(class, nil)); err != nil {
		t.Errorf("an unattended apply over a machine nothing has bootstrapped = %v, want the installs to proceed", err)
	}
}

func TestAnUnattendedApplyWillNotWriteOverWhatAlreadyStands(t *testing.T) {
	t.Parallel()

	read := drifted(t, standingHost(t), recordsHelper)
	refused := refusal(t, admitReplacements(read, Items(read.Class, read.Keys)), providerkit.CodeNotReady)
	if !strings.Contains(refused.Message, recordsHelper) {
		t.Errorf("the refusal says %q, want it to name %s as the thing it would write over", refused.Message, recordsHelper)
	}
	if !strings.Contains(refused.Message, "--yes") {
		t.Errorf("the refusal says %q, want it to name the flag that accepts the write", refused.Message)
	}
}
