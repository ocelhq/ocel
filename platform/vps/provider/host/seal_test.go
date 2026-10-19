package host

import (
	"crypto/rand"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
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

func createKey(t *testing.T, root string) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, string(sealTier), machine.SealKeyFile), key, 0o400); err != nil {
		t.Fatal(err)
	}
}

func TestTheSealKeyIsRootsAloneAndIsWrittenAfterTheHelperThatMintsIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	items := Items(tier, []byte(aKey+"\n"), ssh.AMD64, front.Proxy{})

	key := written(items, machine.KindSealKey, SealKeyPath(tier))
	if key.Name == "" {
		t.Fatalf("nothing in the item set mints %s, so a bootstrapped host seals nothing", SealKeyPath(tier))
	}
	if key.Mode != sealKeyMode || key.Owner != machine.RootOwner {
		t.Errorf("%s is written %04o to %q, want %04o to %s: the deploy login opens values, it does not own the key",
			key.Name, key.Mode, key.Owner, sealKeyMode, machine.RootOwner)
	}
	if len(key.Content) != 0 {
		t.Error("the seal key is written with content from this machine, and a key that leaves the host is no key sealed to it")
	}

	agent := written(items, machine.KindFile, LiveBinary)
	if agent.Name == "" || agent.Owner != machine.RootOwner || agent.Mode&0o022 != 0 {
		t.Errorf("%s is written %04o to %q, and an agent the caller can rewrite is a key the caller can read", LiveBinary, agent.Mode, agent.Owner)
	}
	if at(items, LiveBinary) > at(items, key.Name) {
		t.Error("the seal key is created before the agent that creates it exists")
	}
}

func TestTheDeployLoginIsWhitelistedOnTheHelperAndTheCheckedDockerAndOnNothingBeside(t *testing.T) {
	t.Parallel()

	items := Items(environment.TierProduction, []byte(aKey+"\n"), ssh.AMD64, front.Proxy{})

	var lines []machine.Item
	for _, item := range items {
		if strings.HasPrefix(item.Name, machine.SudoersRoot+"/") {
			lines = append(lines, item)
		}
	}
	if len(lines) != 2 || lines[0].Name != sudoersSeal(environment.TierProduction) || lines[1].Name != sudoersDocker {
		t.Fatalf("a bootstrap writes %v under %s, want the line the seal helper needs and the line the checked docker needs", lines, machine.SudoersRoot)
	}
	if docker := strings.TrimSpace(string(lines[1].Content)); docker != deployUser+" ALL=(root) NOPASSWD: "+LiveBinary+" docker *" {
		t.Errorf("the docker fragment reads %q, want docker reached only through the check", docker)
	}

	fragment := lines[0]
	if fragment.Owner != machine.RootOwner || fragment.Mode != 0o440 {
		t.Errorf("%s is written %04o to %q, want 0440 to %s or sudo refuses to read it", fragment.Name, fragment.Mode, fragment.Owner, machine.RootOwner)
	}
	written := strings.TrimSpace(string(fragment.Content))
	if want := deployUser + " ALL=(root) NOPASSWD: " + LiveBinary + " seal production seal *, " + LiveBinary + " seal production open *"; written != want {
		t.Errorf("the fragment reads %q, want %q: one helper, the tier it seals under, the two verbs a deploy uses and no path beside it", written, want)
	}
	if fragment.Name != sudoersSeal(environment.TierProduction) || strings.ContainsAny(strings.TrimPrefix(fragment.Name, machine.SudoersRoot+"/"), ".~") {
		t.Errorf("the fragment sits at %q, want one file per tier under %s whose name sudo will read: sudoers.d skips names containing '.' or '~'", fragment.Name, machine.SudoersRoot)
	}
	if at(items, principal().Name) > at(items, fragment.Name) {
		t.Error("the sudoers line is written before the login it names exists")
	}
}

func TestTheSurveyReadsTheKeysFingerprintWithoutReadingTheKey(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	fingerprint := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	rendered := machine.KindSealKey + "\t" + SealKeyPath(tier) + "\t400\troot\t" + fingerprint + "\t2026-08-26T09:00:00Z\n"

	observed, seal, err := machine.ReadSurvey(rendered)
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
	item := machine.Item{Kind: machine.KindSealKey, Name: key, Mode: sealKeyMode, Owner: owning(t), Tier: environment.TierProduction}

	if rendered := sh(t, t.TempDir(), sealSurvey(item)); strings.TrimSpace(rendered) != "" {
		t.Fatalf("the survey answered %q where no key exists", rendered)
	}

	createKey(t, root)
	observed, seal, err := machine.ReadSurvey(sh(t, t.TempDir(), sealSurvey(item)))
	if err != nil {
		t.Fatalf("readSurvey() over what the survey script answers = %v", err)
	}
	if len(seal.Fingerprint) != 64 {
		t.Errorf("the survey read %q as the key's fingerprint, want a SHA256", seal.Fingerprint)
	}
	if seal.Algorithm != machine.SealAlgorithm {
		t.Errorf("the survey read the algorithm %q, want %q", seal.Algorithm, machine.SealAlgorithm)
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
	createKey(t, root)
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
	sh(t, stubbed, item.Command())

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
	observed := machine.DigestsOf(Items(tier, keys, ssh.AMD64, front.Proxy{}))
	minted := machine.Seal{Fingerprint: "9f86d081884c7d659a", Algorithm: machine.SealAlgorithm, CreatedAt: "2026-08-26T09:00:00Z"}

	read := Reading{
		Arch: ssh.AMD64,
		Tier: tier, Keys: keys, Present: true, Observed: observed, Seal: minted,
		Stamp: machine.Stamp{State: machine.StateComplete, Digests: observed, Seal: minted},
	}
	if !read.upToDate() {
		t.Fatal("a host exactly as it was applied reads as drifted")
	}

	read.Seal = machine.Seal{Fingerprint: "0000000000000000", Algorithm: machine.SealAlgorithm, CreatedAt: "2026-08-26T10:00:00Z"}
	if read.upToDate() {
		t.Error("a host whose seal key was replaced reads as up to date, so drift in what every secret opens to is invisible")
	}
}

func TestAnApplyOverAReplacedKeyRefusesRatherThanRestampingIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	minted := machine.Seal{Fingerprint: "9f86d081884c7d659a", Algorithm: machine.SealAlgorithm, CreatedAt: "2026-08-26T09:00:00Z"}
	stamped := machine.Stamp{State: machine.StateComplete, Seal: minted}

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
			Seal: machine.Seal{Fingerprint: "0000000000000000", Algorithm: machine.SealAlgorithm},
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

func TestARefusalOverAReplacedKeyNamesTheBootstrapDestroyOfItsTier(t *testing.T) {
	t.Parallel()

	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		minted := machine.Seal{Fingerprint: "9f86d081884c7d659a", Algorithm: machine.SealAlgorithm}
		read := Reading{Tier: tier, Present: true, Stamp: machine.Stamp{State: machine.StateComplete, Seal: minted}}
		err := read.adopting()
		var refused refusal.Refusal
		if !errors.As(err, &refused) {
			t.Fatalf("%s: adopting() = %v, want a refusal", tier, err)
		}
		if want := "`ocel bootstrap destroy " + string(tier) + "`"; !strings.Contains(refused.Message, want) {
			t.Errorf("%s refuses with %q, want it to name %s, the command that removes the tier and its key", tier, refused.Message, want)
		}
	}
}

func TestDestroyNamesTheKeyAsDataBearingAndKeepsTheHelperWhileASiblingRemains(t *testing.T) {
	t.Parallel()

	production, preview := environment.TierProduction, environment.TierPreview
	keys := []byte(aKey + "\n")
	installed := machine.DigestsOf(Items(production, keys, ssh.AMD64, front.Proxy{}))

	alone := removing(Reading{Arch: ssh.AMD64, Tier: production, Keys: keys, Observed: installed}, Reading{Arch: ssh.AMD64, Tier: preview, Observed: map[string]string{}}, appsPresent{})
	key := removalOf(alone, SealKeyPath(production))
	if key.Path == "" {
		t.Fatalf("destroy leaves %s behind, and a key nothing takes is every sealed value still openable", SealKeyPath(production))
	}
	if key.Reason == "" {
		t.Error("destroy takes the seal key with no reason, and the typed confirmation must name what is unrecoverable")
	}
	if index(alone, key.Path) > index(alone, boxapi.TierDir(production)) {
		t.Error("the tier directory is removed before the key it contains is named, so the confirmation names bytes that are already gone")
	}
	for _, singleton := range []string{LiveBinary, sudoersSeal(production)} {
		if removalOf(alone, singleton).Path == "" {
			t.Errorf("destroying the last tier leaves %s behind", singleton)
		}
	}

	beside := machine.DigestsOf(Items(preview, keys, ssh.AMD64, front.Proxy{}))
	shared := removing(Reading{Arch: ssh.AMD64, Tier: production, Keys: keys, Observed: installed}, Reading{Arch: ssh.AMD64, Tier: preview, Keys: keys, Observed: beside}, appsPresent{})
	if removalOf(shared, LiveBinary).Path != "" {
		t.Errorf("destroying one tier takes %s, which an installed sibling still seals through", LiveBinary)
	}
	if removalOf(shared, sudoersSeal(preview)).Path != "" {
		t.Errorf("destroying %s takes %s, the line the installed %s tier seals through", production, sudoersSeal(preview), preview)
	}
	if removalOf(shared, sudoersSeal(production)).Path == "" {
		t.Errorf("destroying %s leaves %s behind, and a line that opens a tier whose key is gone is a grant nothing revokes", production, sudoersSeal(production))
	}
	if removalOf(shared, SealKeyPath(production)).Path == "" {
		t.Error("destroying one tier leaves its own key behind, and a tier is what a key is scoped to")
	}
}

func removalOf(removals []machine.Removal, path string) machine.Removal {
	for _, r := range removals {
		if r.Path == path {
			return r
		}
	}
	return machine.Removal{}
}

func index(removals []machine.Removal, path string) int {
	for i, r := range removals {
		if r.Path == path {
			return i
		}
	}
	return -1
}

func at(items []machine.Item, name string) int {
	for i, item := range items {
		if item.Name == name {
			return i
		}
	}
	return -1
}
