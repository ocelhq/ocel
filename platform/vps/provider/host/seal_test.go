package host

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/conformance"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

const sealTier = "production"

func sealDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 on this machine, and the seal helper reaches openssl through it")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, sealTier), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func rootedHelper(t *testing.T, root string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "seal")
	rooted := bytes.Replace(sealScript, []byte(`SEAL_ROOT = "/etc/ocel"`), []byte("SEAL_ROOT = "+strconv.Quote(root)), 1)
	if bytes.Equal(rooted, sealScript) {
		t.Fatal("the seal helper no longer names /etc/ocel as one constant, so this bench cannot point it at a scratch root")
	}
	if err := os.WriteFile(script, rooted, 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func sealHelperAt(t *testing.T, root, stdin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("python3", append([]string{rootedHelper(t, root), sealTier}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	rendered, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(rendered), 0
	case errors.As(err, &exit):
		return string(rendered), exit.ExitCode()
	default:
		t.Fatalf("run the seal helper: %v\n%s", err, stderr.String())
		return "", 0
	}
}

type scratchHelper struct{ script string }

func (h scratchHelper) Seal(ctx context.Context, what string, tier environment.Tier, argv []string, stdin io.Reader) (string, error) {
	return boxstore.LocalTransport{Elevation: []string{"python3", h.script}}.Seal(ctx, what, tier, argv[1:], stdin)
}

func TestTheBoxCipherSealsAsEveryCipherMust(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if err := os.MkdirAll(filepath.Join(root, string(environment.TierPreview)), 0o755); err != nil {
		t.Fatal(err)
	}
	script := rootedHelper(t, root)
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if out, err := exec.Command("python3", script, string(tier), "init").CombinedOutput(); err != nil {
			t.Fatalf("init %s: %v\n%s", tier, err, out)
		}
	}

	conformance.RunCipher(t, boxstore.NewCipher(scratchHelper{script: script}))
}

func TestTheSealHelperMintsAKeyOnceAndMintsNothingOverIt(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if rendered, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init on a tier with no key exited %d with %q", code, rendered)
	}

	key := filepath.Join(root, sealTier, "seal.key")
	info, err := os.Stat(key)
	if err != nil {
		t.Fatalf("init exited 0 and wrote no key: %v", err)
	}
	if info.Size() != sealKeyBytes {
		t.Errorf("the key is %d bytes, want %d: AES-256 is sealed to nothing narrower", info.Size(), sealKeyBytes)
	}
	if info.Mode().Perm() != sealKeyMode {
		t.Errorf("the key is at mode %04o, want %04o: every secret on this host opens to whoever reads it", info.Mode().Perm(), sealKeyMode)
	}

	minted, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if _, code := sealHelperAt(t, root, "", "init"); code == 0 {
		t.Fatal("a second init exited 0, and a key minted over is every value sealed to the old one lost")
	}
	again, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(minted) {
		t.Error("a refused init moved the key anyway")
	}
}

var bound = seal.AssociatedData{
	{Name: "project", Value: "shop"},
	{Name: "class", Value: sealTier},
	{Name: "environment", Value: "*"},
	{Name: "folder", Value: "/a%2Fb"},
	{Name: "binding", Value: ""},
	{Name: "key", Value: "DATABASE_URL"},
}

var boundFlags = sealFlags(bound)

type argvTaken struct{ argv []string }

func (a *argvTaken) Seal(_ context.Context, _ string, _ environment.Tier, argv []string, _ io.Reader) (string, error) {
	a.argv = argv
	return "", nil
}

func sealArgv(verb string, tier environment.Tier, bound seal.AssociatedData) ([]string, error) {
	taken := &argvTaken{}
	cipher := boxstore.NewCipher(taken)
	var err error
	if verb == "open" {
		_, err = cipher.Open(context.Background(), tier, bound, nil)
	} else {
		_, err = cipher.Seal(context.Background(), tier, bound, nil)
	}
	return taken.argv, err
}

func sealFlags(bound seal.AssociatedData) []string {
	argv, err := sealArgv("seal", sealTier, bound)
	if err != nil {
		panic(err)
	}
	return argv[3:]
}

func TestTheSealHelperRoundTripsAValueAndOpensItNowhereElse(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}

	plaintext := "postgres://example"
	sealed, code := sealHelperAt(t, root, encoded(plaintext), append([]string{"seal"}, boundFlags...)...)
	if code != 0 {
		t.Fatalf("seal exited %d", code)
	}
	if strings.Contains(sealed, encoded(plaintext)) {
		t.Fatal("the helper answered a seal containing the value it was handed")
	}

	opened, code := sealHelperAt(t, root, sealed, append([]string{"open"}, boundFlags...)...)
	if code != 0 {
		t.Fatalf("open under the associated data sealed exited %d", code)
	}
	if got := decoded(t, opened); got != plaintext {
		t.Errorf("open answered %q, want %q", got, plaintext)
	}

	for i, field := range bound {
		moved := slices.Clone(bound)
		moved[i].Value = field.Value + "moved"
		if _, code := sealHelperAt(t, root, sealed, append([]string{"open"}, sealFlags(moved)...)...); code == 0 {
			t.Errorf("a value sealed here opened with another %s, so the associated data authenticates nothing", field.Name)
		}
	}
	reordered := slices.Clone(bound)
	reordered[0], reordered[len(reordered)-1] = reordered[len(reordered)-1], reordered[0]
	if _, code := sealHelperAt(t, root, sealed, append([]string{"open"}, sealFlags(reordered)...)...); code == 0 {
		t.Error("a value sealed here opened with its fields handed in another order, so the order authenticates nothing")
	}
}

func TestTheSealHelperOpensNothingWhoseBytesMoved(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	sealed, code := sealHelperAt(t, root, encoded("postgres://example"), append([]string{"seal"}, boundFlags...)...)
	if code != 0 {
		t.Fatalf("seal exited %d", code)
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sealed))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, code := sealHelperAt(t, root, tampered, append([]string{"open"}, boundFlags...)...); code == 0 {
		t.Fatal("a sealed value whose bytes moved opened anyway")
	}
}

func TestTheSealHelperOpensAValueSealedUnderTheKeyAndAssociatedDataItWasSealedWith(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	key, err := base64.StdEncoding.DecodeString("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "production", "seal.key"), key, 0o400); err != nil {
		t.Fatal(err)
	}
	at := seal.AssociatedData{
		{Name: "project", Value: "shop"},
		{Name: "class", Value: "production"},
		{Name: "environment", Value: "staging"},
		{Name: "folder", Value: "/web"},
		{Name: "binding", Value: ""},
		{Name: "key", Value: "STRIPE_API_KEY"},
	}
	sealed := "oKGio6SlpqeoqaqrlXMjQSy9Z+ARAOShYg78hOZr+SZv+OoHsggDIp1z"

	opened, code := sealHelperAt(t, root, sealed, append([]string{"open"}, sealFlags(at)...)...)
	if code != 0 {
		t.Fatalf("open exited %d, want the value sealed at shop/production/staging/%%2Fweb//STRIPE_API_KEY/ to open: every value on a box is bound to those bytes", code)
	}
	if got := decoded(t, opened); got != "sk_live_secret" {
		t.Errorf("open answered %q, want %q", got, "sk_live_secret")
	}
}

func TestWhatTheSealHelperWritesIsAES256GCMOverTheKeyOnDisk(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	plaintext := "postgres://example"
	rendered, code := sealHelperAt(t, root, encoded(plaintext), append([]string{"seal"}, boundFlags...)...)
	if code != 0 {
		t.Fatalf("seal exited %d", code)
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rendered))
	if err != nil {
		t.Fatal(err)
	}

	key, err := os.ReadFile(filepath.Join(root, sealTier, "seal.key"))
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], bound.Bytes())
	if err != nil {
		t.Fatalf("what the helper sealed does not open as %s over the key it minted: %v", SealAlgorithm, err)
	}
	if string(opened) != plaintext {
		t.Errorf("what the helper sealed opens as %q, want %q", opened, plaintext)
	}
}

func TestTheSealKeyIsRootsAloneAndIsWrittenAfterTheHelperThatMintsIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	items := Items(tier, []byte(aKey+"\n"), ArchAMD64, Front{})

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

	helper := written(items, KindFile, boxstore.SealHelper)
	if helper.Name == "" || helper.Owner != rootOwner || helper.Mode&0o022 != 0 {
		t.Errorf("%s is written %04o to %q, and a helper the caller can rewrite is a key the caller can read", boxstore.SealHelper, helper.Mode, helper.Owner)
	}
	if at(items, boxstore.SealHelper) > at(items, key.Name) {
		t.Error("the seal key is minted before the helper that mints it exists")
	}
}

func TestTheDeployLoginIsWhitelistedOnTheHelperAndOnNothingBeside(t *testing.T) {
	t.Parallel()

	items := Items(environment.TierProduction, []byte(aKey+"\n"), ArchAMD64, Front{})

	var lines []Item
	for _, item := range items {
		if strings.HasPrefix(item.Name, sudoersRoot+"/") {
			lines = append(lines, item)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("a bootstrap writes %d files under %s, want the one line the seal helper needs", len(lines), sudoersRoot)
	}

	fragment := lines[0]
	if fragment.Owner != rootOwner || fragment.Mode != 0o440 {
		t.Errorf("%s is written %04o to %q, want 0440 to %s or sudo refuses to read it", fragment.Name, fragment.Mode, fragment.Owner, rootOwner)
	}
	written := strings.TrimSpace(string(fragment.Content))
	if want := deployUser + " ALL=(root) NOPASSWD: " + boxstore.SealHelper + " production seal *, " + boxstore.SealHelper + " production open *"; written != want {
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
	key := filepath.Join(root, sealTier, "seal.key")
	item := Item{Kind: KindSealKey, Name: key, Mode: sealKeyMode, Owner: owning(t), Tier: environment.TierProduction}

	if rendered := sh(t, t.TempDir(), sealSurvey(item)); strings.TrimSpace(rendered) != "" {
		t.Fatalf("the survey answered %q where no key exists", rendered)
	}

	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
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
	key := filepath.Join(root, sealTier, "seal.key")
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	minted, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}

	stubbed := t.TempDir()
	for _, name := range []string{"chown", "chmod"} {
		body := "#!/bin/sh\nprintf '%s\\n' \"$(basename \"$0\") $*\" >>" + quoted(filepath.Join(stubbed, "log")) + "\n"
		executable(t, filepath.Join(stubbed, name), body)
	}

	item := Item{Kind: KindSealKey, Name: key, Mode: sealKeyMode, Owner: rootOwner, Tier: environment.TierProduction}
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
	observed := digests(Items(tier, keys, ArchAMD64, Front{}))
	minted := Seal{Fingerprint: "9f86d081884c7d659a", Algorithm: SealAlgorithm, CreatedAt: "2026-08-26T09:00:00Z"}

	read := Reading{
		Arch: ArchAMD64,
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
	installed := digests(Items(production, keys, ArchAMD64, Front{}))

	alone := removing(Reading{Arch: ArchAMD64, Tier: production, Keys: keys, Observed: installed}, Reading{Arch: ArchAMD64, Tier: preview, Observed: map[string]string{}}, appsPresent{})
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
	for _, singleton := range []string{boxstore.SealHelper, sudoersSeal(production)} {
		if removalOf(alone, singleton).path == "" {
			t.Errorf("destroying the last tier leaves %s behind", singleton)
		}
	}

	beside := digests(Items(preview, keys, ArchAMD64, Front{}))
	shared := removing(Reading{Arch: ArchAMD64, Tier: production, Keys: keys, Observed: installed}, Reading{Arch: ArchAMD64, Tier: preview, Keys: keys, Observed: beside}, appsPresent{})
	if removalOf(shared, boxstore.SealHelper).path != "" {
		t.Errorf("destroying one tier takes %s, which an installed sibling still seals through", boxstore.SealHelper)
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

func TestTheProviderReachesTheKeyOnlyThroughTheHelperItInstalled(t *testing.T) {
	t.Parallel()

	argv, err := sealArgv("open", environment.TierProduction, seal.AssociatedData{
		{Name: "project", Value: "shop"},
		{Name: "folder", Value: "/apps/web"},
		{Name: "binding", Value: ""},
		{Name: "key", Value: "DATABASE_URL"},
	})
	if err != nil {
		t.Fatalf("sealArgv() = %v", err)
	}
	if argv[0] != boxstore.SealHelper {
		t.Errorf("the provider runs %q, want the helper it installed and nothing beside", argv[0])
	}
	command := words(argv)
	if strings.Contains(command, SealKeyPath(environment.TierProduction)) {
		t.Errorf("the provider names the key on the command line: %q", command)
	}
	want := []string{
		boxstore.SealHelper, string(environment.TierProduction), "open",
		"--project", "shop", "--folder", "/apps/web", "--binding", "", "--key", "DATABASE_URL",
	}
	if !slices.Equal(argv, want) {
		t.Errorf("the provider runs %q, want %q: the tier it seals under, the verb, then every field it is bound to in order", argv, want)
	}
}

func TestTheHelperIsRunInTheShapeTheSudoersLineWhitelists(t *testing.T) {
	t.Parallel()

	argv, err := sealArgv("seal", environment.TierProduction, bound)
	if err != nil {
		t.Fatalf("sealArgv() = %v", err)
	}

	ran := "sudo -n " + words(argv)
	if strings.Contains(ran, "sh -c") {
		t.Fatalf("the deploy login runs %q, and the line in %s whitelists %s, not a shell", ran, sudoersSeal(environment.TierProduction), boxstore.SealHelper)
	}
	if want := "sudo -n " + quoted(boxstore.SealHelper) + " "; !strings.HasPrefix(ran, want) {
		t.Errorf("the deploy login runs %q, want it to begin %q: sudo matches the command it is handed, and nothing else runs", ran, want)
	}
}

func TestAValueSealedToNoTierIsRefusedRatherThanSealedToWhateverTierExists(t *testing.T) {
	t.Parallel()

	_, err := sealArgv("seal", "", bound)
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
		t.Fatalf("sealing under no tier = %v, want a refusal: a key is minted per tier", err)
	}
}

func TestTheSealHelperRefusesAFieldItIsHandedTwice(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	if _, code := sealHelperAt(t, root, encoded("v"), "seal", "--key", "A", "--key", "B"); code == 0 {
		t.Error("the helper sealed a value bound to key twice, and a field named twice is two callers' fields read as one")
	}
}

func TestTheSealHelperRefusesAFlagThatNamesNoField(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	for _, flag := range []string{"key", "--", "--Key", "--a/b"} {
		if _, code := sealHelperAt(t, root, encoded("v"), "seal", flag, "v"); code == 0 {
			t.Errorf("the helper sealed a value handed %q, which names no field", flag)
		}
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

func decoded(t *testing.T, rendered string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rendered))
	if err != nil {
		t.Fatalf("the helper answered %q, which nothing sealed", rendered)
	}
	return string(raw)
}

func TestTheSealHelperReadsItsRootFromNoEnvironmentVariable(t *testing.T) {
	t.Parallel()

	if bytes.Contains(sealScript, []byte("environ")) {
		t.Error("the seal helper reads its environment, and a root-run helper whose key path an environment variable picks is a key path the caller picks")
	}
}

func newSealFailingBench(helper []byte, failed session.Result) *bench {
	b := machine(map[environment.Tier][]Item{environment.TierProduction: {
		{Kind: KindFile, Name: boxstore.SealHelper, Mode: 0o755, Owner: rootOwner, Content: helper},
	}})
	b.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, quoted(boxstore.SealHelper)+" "+quoted(string(environment.TierProduction))) {
			return failed, true
		}
		return session.Result{}, false
	}
	return b
}

var olderHelper = []byte("#!/usr/bin/env python3\n")

func TestASealTheBoxsOwnHelperCannotRunSaysToBootstrapWhenThatHelperIsNotThisBuilds(t *testing.T) {
	t.Parallel()

	b := newSealFailingBench(olderHelper, session.Result{Code: 2, Stderr: "seal: --class is not a coordinate flag\n"})
	_, err := NewCipher(b.host()).Seal(context.Background(), environment.TierProduction, bound, []byte("v"))

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Seal() through a helper older than this build = %v, want a not-ready refusal", err)
	}
	if !strings.Contains(refused.Message, provider.BootstrapCommand(environment.TierProduction)) {
		t.Errorf("Seal() through a helper older than this build said %q, want it to name `%s`: only a bootstrap installs the helper this build speaks to",
			refused.Message, provider.BootstrapCommand(environment.TierProduction))
	}
}

func TestASealTheBoxsOwnHelperCannotRunIsReportedAsItsHelperSaidWhenThatHelperIsThisBuilds(t *testing.T) {
	t.Parallel()

	b := newSealFailingBench(sealScript, session.Result{Code: 2, Stderr: "seal: libcrypto refused the associated data\n"})
	_, err := NewCipher(b.host()).Seal(context.Background(), environment.TierProduction, bound, []byte("v"))

	if err == nil || !strings.Contains(err.Error(), "libcrypto refused the associated data") {
		t.Fatalf("Seal() = %v, want the helper's own reason", err)
	}
	if strings.Contains(err.Error(), provider.BootstrapCommand(environment.TierProduction)) {
		t.Errorf("Seal() through the helper this build installs said %q, and a bootstrap would reinstall the same helper", err)
	}
}

func TestASealSudoRefusesStaysDeniedWhenTheBoxsHelperIsNotThisBuilds(t *testing.T) {
	t.Parallel()

	b := newSealFailingBench(olderHelper, session.Result{Code: 1, Stderr: "sudo: a password is required\n"})
	answerHelper := b.answer
	b.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, "id -u") {
			return session.Result{Stdout: "1000\n"}, true
		}
		return answerHelper(command)
	}
	_, err := NewCipher(b.host()).Seal(context.Background(), environment.TierProduction, bound, []byte("v"))

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("Seal() that sudo refused = %v, want a denied refusal: the helper never ran, so nothing it said shows it is stale", err)
	}
	if strings.Contains(refused.Message, provider.BootstrapCommand(environment.TierProduction)) {
		t.Errorf("Seal() that sudo refused said %q, and the helper never ran to be found stale", refused.Message)
	}
}
