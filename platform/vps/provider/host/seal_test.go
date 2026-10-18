package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const sealTier environment.Tier = "production"

func sealDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 on this machine, and the seal helper reaches openssl through it")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, string(sealTier)), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTheSealKeyIsRootsAloneAndIsWrittenAfterTheHelperThatMintsIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	items := Items(tier, []byte(aKey+"\n"), ssh.AMD64, Front{})

	key := written(items, KindSealKey, SealKeyPath(tier))
	if key.Name == "" {
		t.Fatalf("nothing in the item set mints %s, so a bootstrapped host seals nothing", SealKeyPath(tier))
	}
	if key.Mode != sealKeyMode || key.Owner != rootOwner {
		t.Errorf("%s is written %04o to %q, want %04o to %s: the deploy login opens values, it does not own the key",
			key.Name, key.Mode, key.Owner, sealKeyMode, rootOwner)
	}
	if len(key.Content) != 0 {
		t.Error("the seal key is written with content from this machine, and a key that leaves the host is no key sealed to it")
	}

	agent := written(items, KindFile, LiveBinary)
	if agent.Name == "" || agent.Owner != rootOwner || agent.Mode&0o022 != 0 {
		t.Errorf("%s is written %04o to %q, and an agent the caller can rewrite is a key the caller can read", LiveBinary, agent.Mode, agent.Owner)
	}
	if at(items, LiveBinary) > at(items, key.Name) {
		t.Error("the seal key is created before the agent that creates it exists")
	}
}

func TestTheDeployLoginIsWhitelistedOnTheHelperAndTheCheckedDockerAndOnNothingBeside(t *testing.T) {
	t.Parallel()

	items := Items(environment.TierProduction, []byte(aKey+"\n"), ssh.AMD64, Front{})

	var lines []Item
	for _, item := range items {
		if strings.HasPrefix(item.Name, sudoersRoot+"/") {
			lines = append(lines, item)
		}
	}
	if len(lines) != 2 || lines[0].Name != sudoersSeal(environment.TierProduction) || lines[1].Name != sudoersDocker {
		t.Fatalf("a bootstrap writes %v under %s, want the line the seal helper needs and the line the checked docker needs", lines, sudoersRoot)
	}
	if docker := strings.TrimSpace(string(lines[1].Content)); docker != deployUser+" ALL=(root) NOPASSWD: "+LiveBinary+" docker *" {
		t.Errorf("the docker fragment reads %q, want docker reached only through the check", docker)
	}

	fragment := lines[0]
	if fragment.Owner != rootOwner || fragment.Mode != 0o440 {
		t.Errorf("%s is written %04o to %q, want 0440 to %s or sudo refuses to read it", fragment.Name, fragment.Mode, fragment.Owner, rootOwner)
	}
	written := strings.TrimSpace(string(fragment.Content))
	if want := deployUser + " ALL=(root) NOPASSWD: " + LiveBinary + " seal production seal *, " + LiveBinary + " seal production open *"; written != want {
		t.Errorf("the fragment reads %q, want %q: one helper, the tier it seals under, the two verbs a deploy uses and no path beside it", written, want)
	}
	if fragment.Name != sudoersSeal(environment.TierProduction) || strings.ContainsAny(strings.TrimPrefix(fragment.Name, sudoersRoot+"/"), ".~") {
		t.Errorf("the fragment sits at %q, want one file per tier under %s whose name sudo will read: sudoers.d skips names containing '.' or '~'", fragment.Name, sudoersRoot)
	}
	if at(items, principal().Name) > at(items, fragment.Name) {
		t.Error("the sudoers line is written before the login it names exists")
	}
}

func TestTheSurveyReadsTheKeysFingerprintWithoutReadingTheKey(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	fingerprint := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	rendered := KindSealKey + "\t" + SealKeyPath(tier) + "\t400\troot\t" + fingerprint + "\t2026-08-26T09:00:00Z\n"

	observed, seal, err := readSurvey(rendered)
	if err != nil {
		t.Fatalf("readSurvey() over the row a host answers with = %v", err)
	}
	if seal.Fingerprint != fingerprint {
		t.Errorf("the survey read the fingerprint %q, want %q", seal.Fingerprint, fingerprint)
	}
	if seal.CreatedAt != "2026-08-26T09:00:00Z" {
		t.Errorf("the survey read %q as when the key came into being", seal.CreatedAt)
	}
	if got := observed[sealKey(tier).ID()]; got != sealKey(tier).Digest() {
		t.Errorf("a key unchanged since ocel minted it surveys as %q, want %q: the bytes of a key are never what says it is current", got, sealKey(tier).Digest())
	}
}

func TestTheSurveyTheHostRunsAnswersForAKeyThatExistsAndSaysNothingForOneThatDoesNot(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	key := filepath.Join(root, string(sealTier), "seal.key")
	item := Item{Kind: KindSealKey, Name: key, Mode: sealKeyMode, Owner: owning(t), Tier: environment.TierProduction}

	if rendered := sh(t, t.TempDir(), sealSurvey(item)); strings.TrimSpace(rendered) != "" {
		t.Fatalf("the survey answered %q where no key exists", rendered)
	}

	if err := boxapi.CreateKey(root, sealTier); err != nil {
		t.Fatal(err)
	}
	observed, seal, err := readSurvey(sh(t, t.TempDir(), sealSurvey(item)))
	if err != nil {
		t.Fatalf("readSurvey() over what the survey script answers = %v", err)
	}
	if len(seal.Fingerprint) != 64 {
		t.Errorf("the survey read %q as the key's fingerprint, want a SHA256", seal.Fingerprint)
	}
	if seal.Algorithm != SealAlgorithm {
		t.Errorf("the survey read the algorithm %q, want %q", seal.Algorithm, SealAlgorithm)
	}
	if !strings.HasSuffix(seal.CreatedAt, "Z") {
		t.Errorf("the survey read %q as when the key came into being, want a UTC instant", seal.CreatedAt)
	}
	if got := observed[item.ID()]; got != item.Digest() {
		t.Errorf("a key unchanged since ocel minted it surveys as %q, want %q", got, item.Digest())
	}
}

func TestWritingAKeyThatExistsReassertsItsPostureAndMintsNothing(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	key := filepath.Join(root, string(sealTier), "seal.key")
	if err := boxapi.CreateKey(root, sealTier); err != nil {
		t.Fatal(err)
	}
	minted, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}

	stubbed := t.TempDir()
	for _, name := range []string{"chown", "chmod"} {
		body := "#!/bin/sh\nprintf '%s\\n' \"$(basename \"$0\") $*\" >>" + shell.Quote(filepath.Join(stubbed, "log")) + "\n"
		executable(t, filepath.Join(stubbed, name), body)
	}

	item := sealKey(environment.TierProduction)
	item.Name = key
	sh(t, stubbed, item.command())

	log := ran(t, stubbed)
	for _, want := range []string{"chown root:root " + key, "chmod 0400 " + key} {
		if !strings.Contains(log, want) {
			t.Errorf("writing a key that exists ran\n%s\nwant it to run %q", log, want)
		}
	}
	again, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(minted) {
		t.Error("writing a key that exists minted a new one, and every value sealed to the old one went with it")
	}
}

func owning(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(sh(t, t.TempDir(), "id -un"))
}

func TestAReplacedKeyIsDriftThoughEveryPathIsStillAsItWasWritten(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	keys := []byte(aKey + "\n")
	observed := digests(Items(tier, keys, ssh.AMD64, Front{}))
	minted := Seal{Fingerprint: "9f86d081884c7d659a", Algorithm: SealAlgorithm, CreatedAt: "2026-08-26T09:00:00Z"}

	read := Reading{
		Arch: ssh.AMD64,
		Tier: tier, Keys: keys, Present: true, Observed: observed, Seal: minted,
		Stamp: Stamp{State: StateComplete, Digests: observed, Seal: minted},
	}
	if !read.upToDate() {
		t.Fatal("a host exactly as it was applied reads as drifted")
	}

	read.Seal = Seal{Fingerprint: "0000000000000000", Algorithm: SealAlgorithm, CreatedAt: "2026-08-26T10:00:00Z"}
	if read.upToDate() {
		t.Error("a host whose seal key was replaced reads as up to date, so drift in what every secret opens to is invisible")
	}
}

func TestAnApplyOverAReplacedKeyRefusesRatherThanRestampingIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	minted := Seal{Fingerprint: "9f86d081884c7d659a", Algorithm: SealAlgorithm, CreatedAt: "2026-08-26T09:00:00Z"}
	stamped := Stamp{State: StateComplete, Seal: minted}

	for name, read := range map[string]Reading{
		"a first apply, where nothing was ever minted":  {Tier: tier},
		"an apply over the key the stamp records":       {Tier: tier, Present: true, Seal: minted, Stamp: stamped},
		"an apply over a host stamped before seal keys": {Tier: tier, Present: true, Seal: minted},
	} {
		if err := read.adopting(); err != nil {
			t.Errorf("%s = %v, want the apply that stamps it", name, err)
		}
	}

	for name, read := range map[string]Reading{
		"a key replaced beneath the stamp": {
			Tier: tier, Present: true, Stamp: stamped,
			Seal: Seal{Fingerprint: "0000000000000000", Algorithm: SealAlgorithm},
		},
		"a key taken from beneath the stamp": {Tier: tier, Present: true, Stamp: stamped},
	} {
		err := read.adopting()
		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
			t.Fatalf("%s = %v, want a refusal: adopting it silently is every sealed value unopenable and nothing said", name, err)
		}
		if !strings.Contains(refused.Message, minted.Fingerprint) {
			t.Errorf("%s refuses with %q, which never names the key the stamp records", name, refused.Message)
		}
	}
}

func TestDestroyNamesTheKeyAsDataBearingAndKeepsTheHelperWhileASiblingRemains(t *testing.T) {
	t.Parallel()

	production, preview := environment.TierProduction, environment.TierPreview
	keys := []byte(aKey + "\n")
	installed := digests(Items(production, keys, ssh.AMD64, Front{}))

	alone := removing(Reading{Arch: ssh.AMD64, Tier: production, Keys: keys, Observed: installed}, Reading{Arch: ssh.AMD64, Tier: preview, Observed: map[string]string{}}, appsPresent{})
	key := removalOf(alone, SealKeyPath(production))
	if key.path == "" {
		t.Fatalf("destroy leaves %s behind, and a key nothing takes is every sealed value still openable", SealKeyPath(production))
	}
	if key.reason == "" {
		t.Error("destroy takes the seal key with no reason, and the typed confirmation must name what is unrecoverable")
	}
	if index(alone, key.path) > index(alone, TierDir(production)) {
		t.Error("the tier directory is removed before the key it contains is named, so the confirmation names bytes that are already gone")
	}
	for _, singleton := range []string{LiveBinary, sudoersSeal(production)} {
		if removalOf(alone, singleton).path == "" {
			t.Errorf("destroying the last tier leaves %s behind", singleton)
		}
	}

	beside := digests(Items(preview, keys, ssh.AMD64, Front{}))
	shared := removing(Reading{Arch: ssh.AMD64, Tier: production, Keys: keys, Observed: installed}, Reading{Arch: ssh.AMD64, Tier: preview, Keys: keys, Observed: beside}, appsPresent{})
	if removalOf(shared, LiveBinary).path != "" {
		t.Errorf("destroying one tier takes %s, which an installed sibling still seals through", LiveBinary)
	}
	if removalOf(shared, sudoersSeal(preview)).path != "" {
		t.Errorf("destroying %s takes %s, the line the installed %s tier seals through", production, sudoersSeal(preview), preview)
	}
	if removalOf(shared, sudoersSeal(production)).path == "" {
		t.Errorf("destroying %s leaves %s behind, and a line that opens a tier whose key is gone is a grant nothing revokes", production, sudoersSeal(production))
	}
	if removalOf(shared, SealKeyPath(production)).path == "" {
		t.Error("destroying one tier leaves its own key behind, and a tier is what a key is scoped to")
	}
}

func removalOf(removals []removal, path string) removal {
	for _, r := range removals {
		if r.path == path {
			return r
		}
	}
	return removal{}
}

func index(removals []removal, path string) int {
	for i, r := range removals {
		if r.path == path {
			return i
		}
	}
	return -1
}

func at(items []Item, name string) int {
	for i, item := range items {
		if item.Name == name {
			return i
		}
	}
	return -1
}
