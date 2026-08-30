package host

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const aKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample bootstrap@laptop"

func TestTheClassTierIsRootsAndTheStateTierIsTheDeployPrincipalsAlone(t *testing.T) {
	t.Parallel()

	items := Items(providerkit.ClassProduction, []byte(aKey+"\n"), ArchAMD64)
	owners := map[string]string{}
	for _, item := range items {
		owners[item.Name] = item.Owner
	}
	for name, want := range map[string]string{
		classRoot:                               rootOwner,
		ClassDir(providerkit.ClassProduction):   rootOwner,
		helperRoot:                              rootOwner,
		recordsHelper:                           rootOwner,
		stateRoot:                               deployUser,
		sshDir:                                  deployUser,
		authorizedKeys:                          deployUser,
		StateDir(providerkit.ClassProduction):   deployUser,
		RecordsDir(providerkit.ClassProduction): deployUser,
	} {
		if owners[name] != want {
			t.Errorf("%s is written to %q, want %q: a path with two owners is a path with none", name, owners[name], want)
		}
	}
}

func TestThePrincipalIsWrittenBeforeAnythingItOwns(t *testing.T) {
	t.Parallel()

	items := Items(providerkit.ClassProduction, []byte(aKey+"\n"), ArchAMD64)
	account := slices.IndexFunc(items, func(item Item) bool { return item.Kind == KindUser })
	if account < 0 {
		t.Fatal("nothing in the item set creates the deploy principal")
	}
	for i, item := range items {
		if item.Owner == deployUser && i < account {
			t.Errorf("%s is written before %s exists, and install names an owner the host does not hold", item.ID(), deployUser)
		}
	}
}

func TestTheDeployKeysAreTheOnesTheItemCarries(t *testing.T) {
	t.Parallel()

	keys := []byte(aKey + "\n")
	var written Item
	for _, item := range Items(providerkit.ClassProduction, keys, ArchAMD64) {
		if item.Name == authorizedKeys {
			written = item
		}
	}
	if string(written.Content) != string(keys) {
		t.Errorf("%s is written with %q, want the keys the deploy login answers to", authorizedKeys, written.Content)
	}
	if written.Mode != 0o600 {
		t.Errorf("%s is written at %04o, want a mode sshd will read", authorizedKeys, written.Mode)
	}
}

func TestKeysNamedByPathAreReadFromItAndTidied(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(path, []byte("# a comment\n\n  "+aKey+"  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := (Keys{Path: path}).named()
	if err != nil {
		t.Fatalf("named() = %v", err)
	}
	if string(keys) != aKey+"\n" {
		t.Errorf("named() = %q, want the key alone so that what stands can be compared to what would be written", keys)
	}
}

func TestKeysNamedByAPathThatHoldsNoneAreRefused(t *testing.T) {
	t.Parallel()

	empty := filepath.Join(t.TempDir(), "empty.pub")
	if err := os.WriteFile(empty, []byte("# nothing but a comment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"a path nothing wrote": filepath.Join(t.TempDir(), "absent.pub"),
		"a file with no key":   empty,
	} {
		if _, err := (Keys{Path: path}).named(); err == nil {
			t.Errorf("named() of %s succeeded, and a deploy login with no key answers to nobody", name)
		}
	}
}

func TestAPrincipalNothingHasCreatedIsPlannedAndOneThatStandsIsKept(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	fresh := planFor(planned(Reading{Arch: ArchAMD64, Class: class, Keys: keys, Observed: map[string]string{}}), principal().ID())
	if fresh.Action != providerkit.ActionCreate {
		t.Errorf("a host with no %s plans %q, want it created", deployUser, fresh.Action)
	}

	standing := Reading{Arch: ArchAMD64, Class: class, Keys: keys, Observed: digests(Items(class, keys, ArchAMD64))}
	if kept := planFor(planned(standing), principal().ID()); kept.Action != providerkit.ActionKeep {
		t.Errorf("a host whose principal stands as ocel writes it plans %q, want it kept", kept.Action)
	}
	for _, change := range planned(standing) {
		if change.Action != providerkit.ActionKeep {
			t.Errorf("%s plans %q over a host that stands as ocel wrote it", change.Name, change.Action)
		}
	}
}

func TestKeysThatChangedRePlanTheAuthorizedKeysAndNothingBeside(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := Items(class, []byte(aKey+"\n"), ArchAMD64)
	moved := Reading{Arch: ArchAMD64, Class: class, Keys: []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOther other@laptop\n"), Observed: digests(stood)}
	for _, change := range planned(moved) {
		want := providerkit.ActionKeep
		if change.Name == authorizedKeys {
			want = providerkit.ActionUpdate
		}
		if change.Action != want {
			t.Errorf("%s plans %q after the deploy key moved, want %q", change.Name, change.Action, want)
		}
	}
}

func TestThePrincipalGoesWithTheLastClassAndStandsWhileASiblingDoes(t *testing.T) {
	t.Parallel()

	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	keys := []byte(aKey + "\n")
	held := digests(Items(production, keys, ArchAMD64))

	alone := removing(Reading{Arch: ArchAMD64, Class: production, Keys: keys, Observed: held}, Reading{Arch: ArchAMD64, Class: preview, Observed: map[string]string{}})
	taken := slices.IndexFunc(alone, func(r removal) bool { return r.kind == KindUser && r.path == deployUser })
	if taken < 0 {
		t.Fatal("destroying the last class leaves the deploy principal behind, and a login nothing uses is a login nobody revokes")
	}
	if state := slices.IndexFunc(alone, func(r removal) bool { return r.path == stateRoot }); state > taken {
		t.Error("the principal is removed before its home, and userdel over a home ocel still holds is a home nothing owns")
	}

	beside := digests(Items(preview, keys, ArchAMD64))
	shared := removing(Reading{Arch: ArchAMD64, Class: production, Keys: keys, Observed: held}, Reading{Arch: ArchAMD64, Class: preview, Keys: keys, Observed: beside})
	for _, r := range shared {
		if r.kind == KindUser {
			t.Error("destroying one class takes the deploy principal a standing sibling still deploys as")
		}
	}
}

func TestNothingIsEverTakenAfterTheStampButTheRootAboveIt(t *testing.T) {
	t.Parallel()

	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	keys := []byte(aKey + "\n")
	standing := Reading{Arch: ArchAMD64, Class: production, Keys: keys, Observed: digests(Items(production, keys, ArchAMD64))}

	for name, sibling := range map[string]Reading{
		"the last class on the host": {Class: preview, Observed: map[string]string{}},
		"a class beside its sibling": {Class: preview, Keys: keys, Observed: digests(Items(preview, keys, ArchAMD64))},
	} {
		taken := removing(standing, sibling)
		stamp := index(taken, ClassDir(production))
		if stamp < 0 {
			t.Fatalf("destroying %s never takes %s, and the stamp stands over a host that holds nothing", name, ClassDir(production))
		}
		for at, r := range taken {
			if r.action != providerkit.ActionDelete || at <= stamp || r.path == classRoot {
				continue
			}
			t.Errorf("destroying %s takes %s after the stamp, so an interrupted destroy leaves a host that lies about what it carries", name, r.path)
		}
	}
}

func TestDestroyOfAHalfWrittenHostNamesWhatStandsAndNothingBeside(t *testing.T) {
	t.Parallel()

	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	half := map[string]string{}
	for _, item := range ClassItems(production) {
		half[item.ID()] = item.Digest()
	}
	taken := removing(
		Reading{Arch: ArchAMD64, Class: production, Observed: half},
		Reading{Arch: ArchAMD64, Class: preview, Observed: map[string]string{}},
	)
	for _, r := range taken {
		if _, stands := half[r.kind+" "+r.path]; !stands {
			t.Errorf("destroy takes %s %s off a host that never had it, and an apply that died halfway needs no mode of its own", r.kind, r.path)
		}
	}
	if last := index(taken, classRoot); last != len(taken)-1 {
		t.Errorf("destroy of a half-written host takes %s at %d of %d rows, want the shared root taken after the class tier beneath it",
			classRoot, last, len(taken))
	}
	if index(taken, ClassDir(production)) > index(taken, classRoot) {
		t.Error("the class directory carrying the stamp is taken after the root above it, so an interrupted destroy loses what the host says it is")
	}
}

func TestDestroyLeavesTheTrustStoreAloneAndSpellsTheLineThatEditsIt(t *testing.T) {
	t.Parallel()

	forget := "ssh-keygen -R '[203.0.113.10]:2222' -f /home/ada/.ssh/known_hosts"
	note := leavingKnownHosts(forget)
	if !strings.Contains(note, forget) {
		t.Errorf("the destroy note reads %q, and a user who wants the entry gone is never told what to run", note)
	}
	if !strings.Contains(note, "known_hosts") {
		t.Errorf("the destroy note reads %q, and it never says which of the user's own files ocel left alone", note)
	}
}

func planFor(changes []providerkit.Change, id string) providerkit.Change {
	for _, change := range changes {
		if change.Kind+" "+change.Name == id {
			return change
		}
	}
	return providerkit.Change{}
}

func TestTheAccountFactsCarryEveryFieldTheWriteSets(t *testing.T) {
	t.Parallel()

	facts := string(principal().Content)
	for _, want := range []string{deployShell, stateRoot, dockerGroup, "locked"} {
		if !strings.Contains(facts, want) {
			t.Errorf("the account digest reads %q, which says nothing about %q, so drift there would never re-plan", facts, want)
		}
	}
}

func TestAKeyPathRootedAtHomeIsReadFromTheHomeThisRunHas(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "ocel-deploy.pub"), []byte(aKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := (Keys{Path: "~/.ssh/ocel-deploy.pub"}).named()
	if err != nil {
		t.Fatalf("a key named the way the option documents it = %v", err)
	}
	if string(keys) != aKey+"\n" {
		t.Errorf("the deploy login would answer to %q, want the key under the home ~ names", keys)
	}
}

func TestAKeyPathRelativeToNothingIsRefusedRatherThanGuessedAt(t *testing.T) {
	t.Parallel()

	_, err := (Keys{Path: "deploy.pub"}).named()
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("a relative key path = %v, want a refusal that says ocel is told no directory to resolve it against", err)
	}
	if !strings.Contains(refusal.Error(), "~/") {
		t.Errorf("the refusal reads %q, and it never says what to spell instead", refusal.Error())
	}
}

func TestOnlyTheOwnerDrawnSurveyPassesOverAFileItCannotRead(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("this test runs as root, which reads every mode, and the login a heal is drawn as is not root")
	}
	root := t.TempDir()
	readable := filepath.Join(root, "records")
	if err := os.WriteFile(readable, []byte("what every login may hash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sealed := filepath.Join(root, proxyHelperName)
	if err := os.WriteFile(sealed, []byte("what root alone may hash\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatal(err)
	}

	items := []Item{
		{Kind: KindFile, Name: readable, Mode: 0o644, Owner: stateOwner},
		{Kind: KindFile, Name: sealed, Mode: 0o750, Owner: rootOwner},
	}
	whole, _, err := readSurvey(sh(t, root, survey(items)))
	if err != nil {
		t.Fatalf("the survey every write is planned against = %v over a tier holding a file this login may not read", err)
	}
	if _, held := whole[KindFile+" "+readable]; !held {
		t.Fatalf("the survey read nothing at all for %s, so what it says about %s means nothing", readable, sealed)
	}
	if reported := whole[KindFile+" "+sealed]; reported == items[1].Digest() {
		t.Fatalf("the survey hashed %s without reading it, so this machine is not the one this test needs", sealed)
	}
	if _, held := whole[KindFile+" "+sealed]; !held {
		t.Errorf("the survey every write is planned against reports nothing for %s, and absence is a lie on the reading a plan is drawn from: what it cannot read is drift it must name, not a path that is gone",
			sealed)
	}
	owned, _, err := readSurvey(sh(t, root, surveyOwned(items)))
	if err != nil {
		t.Fatal(err)
	}
	if _, held := owned[KindFile+" "+sealed]; held {
		t.Errorf("the survey a heal draws reports %s, and a file the drawing login could not hash reads as one that moved: heal denies the whole set over a helper root owns and nothing else may read",
			sealed)
	}
}

func TestASymlinkWhereAnItemsPathShouldBeIsRefusedRatherThanFollowed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	pointed := filepath.Join(root, "state")
	if err := os.Symlink(elsewhere, pointed); err != nil {
		t.Fatal(err)
	}

	for name, script := range map[string]string{
		"a path the survey stats": survey([]Item{dir(pointed, 0o750, stateOwner, "")}),
		"the seal key's own probe": sealSurvey(Item{
			Kind: KindSealKey, Name: pointed, Mode: sealKeyMode, Owner: rootOwner, Class: providerkit.ClassProduction,
		}),
	} {
		_, _, err := readSurvey(sh(t, root, script))
		var refused providerkit.Refusal
		if !errors.As(err, &refused) {
			t.Fatalf("%s over a symlink = %v, want a refusal the CLI can render", name, err)
		}
		if !strings.Contains(refused.Message, pointed) || !strings.Contains(refused.Message, elsewhere) {
			t.Errorf("%s says %q, want it to name both %s and what it points at", name, refused.Message, elsewhere)
		}
	}
}
