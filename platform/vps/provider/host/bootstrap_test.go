package host

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const testVendor provider.Vendor = "vps"

func currentHost() Reading {
	class := edge.ClassProduction
	keys := []byte(aKey + "\n")
	return Reading{
		Arch:     ArchAMD64,
		Class:    class,
		Present:  true,
		Keys:     keys,
		Stamp:    Stamp{Schema: provider.BootstrapSchema, State: StateComplete, Digests: digests(Items(class, keys, ArchAMD64, Front{}))},
		Observed: digests(Items(class, keys, ArchAMD64, Front{})),
	}
}

func drifted(t *testing.T, read Reading, name string) Reading {
	t.Helper()

	for _, item := range Items(read.Class, read.Keys, ArchAMD64, Front{}) {
		if item.Name != name {
			continue
		}
		read.Observed[item.ID()] = "a digest nothing this ocel writes produces"
		return read
	}
	t.Fatalf("nothing in the item set is named %s", name)
	return read
}

func refusalOf(t *testing.T, err error, code refusal.Code) refusal.Refusal {
	t.Helper()

	var refused refusal.Refusal
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

	read := drifted(t, currentHost(), RecordsDir(edge.ClassProduction))
	work, _, err := healable(read)
	if err != nil {
		t.Fatalf("healable() over a drifted record tier = %v, want the deploy login's own state reasserted", err)
	}
	if len(work) != 1 || work[0].Name != RecordsDir(edge.ClassProduction) {
		t.Fatalf("healable() = %v, want only %s", ids(work), RecordsDir(edge.ClassProduction))
	}
}

func TestHealRefusesAMixedSetWholeRatherThanDoingThePartItMay(t *testing.T) {
	t.Parallel()

	read := drifted(t, drifted(t, currentHost(), RecordsDir(edge.ClassProduction)), recordsHelper)
	work, _, err := healable(read)
	refused := refusalOf(t, err, refusal.CodeDenied)
	if !strings.Contains(refused.Message, recordsHelper) {
		t.Errorf("the refusal says %q, want it to name %s as what heal may not write", refused.Message, recordsHelper)
	}
	if len(work) != 0 {
		t.Errorf("healable() = %v alongside its refusal, want a mixed set refused whole", ids(work))
	}
}

func TestHealRefusesEveryItemOutsideTheRecordTier(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	for _, name := range []string{
		ClassDir(class), SealKeyPath(class), SealHelper, sudoersSeal(class), deployUser, sshDir, authorizedKeys,
	} {
		read := drifted(t, currentHost(), name)
		refused := refusalOf(t, second(healable(read)), refusal.CodeDenied)
		if !strings.Contains(refused.Message, name) {
			t.Errorf("heal over a drifted %s says %q, want it named as what heal may not write", name, refused.Message)
		}
	}
}

func TestHealLeavesWhatADaemonReportsRatherThanRefusingOverIt(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	reported := []string{dockerEngine, dockerUnit, ProxyNetwork, caddy.Container, SwitchboardContainer}
	for _, name := range reported {
		read := drifted(t, drifted(t, currentHost(), RecordsDir(class)), name)
		work, left, err := healing(read, true)
		if err != nil {
			t.Fatalf("heal over a box whose %s is not as the stamp records = %v, want it left: heal starts nothing and a daemon reports what it runs, so a stopped one is not drift heal can refuse over",
				name, err)
		}
		if len(work) != 1 || work[0].Name != RecordsDir(class) {
			t.Errorf("healing() over a drifted %s = %v, want only the record tier", name, ids(work))
		}
		if !slices.ContainsFunc(left, func(id string) bool { return strings.HasSuffix(id, " "+name) }) {
			t.Errorf("heal left %v over a drifted %s, and a box told nothing about what heal declined is one nobody can read the exit code of", left, name)
		}
	}

	for _, item := range Items(class, []byte(aKey+"\n"), ArchAMD64, Front{}) {
		if daemonState(item) && deployOwned(item) {
			t.Errorf("%s is both a daemon's to report and heal's to write, and the two dispositions cannot both apply", item.ID())
		}
	}
}

func TestHealWillNotReassertRecordsSealedToAKeyThatIsGone(t *testing.T) {
	t.Parallel()

	read := drifted(t, currentHost(), RecordsDir(edge.ClassProduction))
	read.Stamp.Seal = Seal{Fingerprint: "the key every value this class stores was sealed to"}
	delete(read.Observed, sealKey(read.Class).ID())
	refused := refusalOf(t, second(healing(read, true)), refusal.CodeInvalid)
	if !strings.Contains(refused.Message, SealKeyPath(read.Class)) {
		t.Errorf("heal over a class whose seal key vanished says %q, want it to name the key: reasserting the records and calling the bootstrap refreshed loses the failure until unseal time", refused.Message)
	}
}

func TestHealReadsAKeyItCannotOpenAsTheKeyInPlace(t *testing.T) {
	t.Parallel()

	read := drifted(t, currentHost(), RecordsDir(edge.ClassProduction))
	read.Stamp.Seal = Seal{Fingerprint: "the key the stamp records"}
	read.Seal = Seal{}
	if _, _, err := healing(read, true); err != nil {
		t.Errorf("heal driven by a login that cannot read the key's bytes = %v, want the key in place taken as the current key: nothing but root ever writes it", err)
	}
}

func TestHealRunsTheReplacementRefusingGateTheRestOfApplyRuns(t *testing.T) {
	t.Parallel()

	read := drifted(t, currentHost(), RecordsDir(edge.ClassProduction))
	work, _, err := healing(read, true)
	if err != nil {
		t.Fatalf("a replacement-refusing heal over a drifted record tier = %v, want the converge to proceed", err)
	}
	if err := refuseReplacements(read, work); err != nil {
		t.Errorf("the gate over what a replacement-refusing heal admitted = %v, want heal to have refused it before writing a byte", err)
	}
}

func TestHealIsNotWedgedByWhatItsOwnLoginCannotSee(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	read := drifted(t, currentHost(), RecordsDir(class))
	var unread []string
	for _, item := range Items(class, read.Keys, ArchAMD64, Front{}) {
		hidden := item.Kind == KindFile && item.Owner == rootOwner && item.Mode&0o004 == 0
		if !hidden && item.Kind != KindUser {
			continue
		}
		unread = append(unread, item.ID())
		delete(read.Observed, item.ID())
	}
	for _, hidden := range []string{KindFile + " " + sudoersSeal(edge.ClassProduction)} {
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

	class := edge.ClassProduction
	installed := bootstrapped(t, class)
	records := RecordsDir(class)
	for at, item := range installed {
		if item.Kind == KindDir && item.Name == records {
			installed[at].Mode = 0o700
		}
	}
	box := machine(map[edge.Class][]Item{class: installed})
	box.facts = session.Facts{Arch: "x86_64"}
	box.floor = refusal.Refuse(refusal.CodeDenied,
		"ada@ocelbox can neither act as root nor run sudo without a password, and bootstrap writes as root throughout")
	box.answer = func(command string) (session.Result, bool) {
		if command != "cat ~/.ssh/authorized_keys 2>/dev/null" {
			return session.Result{}, false
		}
		return session.Result{Stdout: aKey + "\n"}, true
	}

	err := NewBootstrap(box.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Class: class, WrittenBy: "the-suite", Heal: true, RefuseReplacements: true}, nil)
	if err != nil {
		t.Fatalf("heal driven by the deploy login = %v, want what that login owns reasserted without asking for root", err)
	}
	ran := box.commands()
	if !slices.ContainsFunc(ran, func(command string) bool {
		return strings.HasPrefix(command, "install -d ") && strings.HasSuffix(command, quoted(records))
	}) {
		t.Errorf("heal never wrote %s back:\n%s", records, strings.Join(ran, "\n"))
	}
	for _, command := range ran {
		if strings.HasPrefix(command, "sudo ") {
			t.Errorf("heal ran %q, and the login it runs as has no sudo at all", command)
		}
	}
}

func TestHealNeverFinishesAnApplyThatDiedMidWay(t *testing.T) {
	t.Parallel()

	read := drifted(t, currentHost(), RecordsDir(edge.ClassProduction))
	read.Stamp.State = StateApplying
	refused := refusalOf(t, second(healable(read)), refusal.CodeDenied)
	if !strings.Contains(refused.Message, StampPath(read.Class)) {
		t.Errorf("heal over an unfinished apply says %q, want it to name the stamp that says so", refused.Message)
	}
}

func TestHealHasNothingToReassertWhereNoBootstrapRan(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	fresh := Reading{Arch: ArchAMD64, Class: class, Observed: map[string]string{}}
	refusalOf(t, second(healable(fresh)), refusal.CodeDenied)
}

func second(_ []Item, _ []string, err error) error { return err }

func ids(items []Item) []string {
	var out []string
	for _, item := range items {
		out = append(out, item.ID())
	}
	return out
}

func TestAReplacementRefusingApplyInstallsWhatIsAbsent(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	fresh := Reading{Arch: ArchAMD64, Class: class, Observed: map[string]string{}}
	if err := refuseReplacements(fresh, Items(class, nil, ArchAMD64, Front{})); err != nil {
		t.Errorf("a replacement-refusing apply over a machine nothing has bootstrapped = %v, want the installs to proceed", err)
	}
}

func TestAReplacementRefusingApplyWillNotWriteOverWhatAlreadyExists(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	for _, name := range []string{recordsHelper, SealKeyPath(class), deployUser, dockerEngine} {
		read := drifted(t, currentHost(), name)
		refused := refusalOf(t, refuseReplacements(read, Items(read.Class, read.Keys, ArchAMD64, Front{})), refusal.CodeNotReady)
		if !strings.Contains(refused.Message, name) {
			t.Errorf("the refusal says %q, want it to name %s as the thing it would write over", refused.Message, name)
		}
		if !strings.Contains(refused.Message, "--yes") {
			t.Errorf("the refusal says %q, want it to name the flag that accepts the write", refused.Message)
		}
	}
}

func TestAReplacementRefusingApplyConvergesAHostRatherThanRefusingEveryChange(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	for _, name := range []string{dockerUnit, RecordsDir(class), stateRoot, helperRoot} {
		read := drifted(t, currentHost(), name)
		if err := refuseReplacements(read, Items(read.Class, read.Keys, ArchAMD64, Front{})); err != nil {
			t.Errorf("a replacement-refusing apply over a host whose %s has moved = %v, want a converge that destroys nothing to proceed", name, err)
		}
	}
}

func TestNothingHealMayWriteIsAReplacementClassChange(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	for _, item := range Items(class, []byte(aKey+"\n"), ArchAMD64, Front{}) {
		if deployOwned(item) && replacing(item) {
			t.Errorf("heal may write %s and writing it replaces rather than converges, so heal, the one path that refuses replacements, would rebuild it", item.ID())
		}
	}
}
