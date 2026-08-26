package host

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const sealClass = "production"

func sealDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3 on this machine, and the seal helper reaches openssl through it")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, sealClass), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func sealHelperAt(t *testing.T, root, stdin string, args ...string) (string, int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "seal")
	if err := os.WriteFile(script, sealScript, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", append([]string{script, sealClass}, args...)...)
	cmd.Env = append(os.Environ(), "OCEL_SEAL_ROOT="+root)
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

func TestTheSealHelperMintsAKeyOnceAndMintsNothingOverIt(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if rendered, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init on a class carrying no key exited %d with %q", code, rendered)
	}

	key := filepath.Join(root, sealClass, "seal.key")
	held, err := os.Stat(key)
	if err != nil {
		t.Fatalf("init exited 0 and wrote no key: %v", err)
	}
	if held.Size() != sealKeyBytes {
		t.Errorf("the key is %d bytes, want %d: AES-256 is sealed to nothing narrower", held.Size(), sealKeyBytes)
	}
	if held.Mode().Perm() != sealKeyMode {
		t.Errorf("the key stands at %04o, want %04o: every secret on this host opens to whoever reads it", held.Mode().Perm(), sealKeyMode)
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

var bound = providerkit.Coordinate{
	Project: "shop",
	Class:   sealClass,
	Env:     "*",
	Folder:  "/a%2Fb",
	Name:    "DATABASE_URL",
}

var aCoordinate = sealFlags(bound)

func sealFlags(at providerkit.Coordinate) []string {
	argv, err := sealArgv("seal", at)
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
	sealed, code := sealHelperAt(t, root, encoded(plaintext), append([]string{"seal"}, aCoordinate...)...)
	if code != 0 {
		t.Fatalf("seal exited %d", code)
	}
	if strings.Contains(sealed, encoded(plaintext)) {
		t.Fatal("the helper answered a seal carrying the value it was handed")
	}

	opened, code := sealHelperAt(t, root, sealed, append([]string{"open"}, aCoordinate...)...)
	if code != 0 {
		t.Fatalf("open at the coordinate sealed exited %d", code)
	}
	if got := decoded(t, opened); got != plaintext {
		t.Errorf("open answered %q, want %q", got, plaintext)
	}

	for name, moved := range map[string]providerkit.Coordinate{
		"another project":     {Project: "other", Class: bound.Class, Env: bound.Env, Folder: bound.Folder, Name: bound.Name},
		"another environment": {Project: bound.Project, Class: bound.Class, Env: "staging", Folder: bound.Folder, Name: bound.Name},
		"another folder":      {Project: bound.Project, Class: bound.Class, Env: bound.Env, Folder: "/a/b", Name: bound.Name},
		"another key":         {Project: bound.Project, Class: bound.Class, Env: bound.Env, Folder: bound.Folder, Name: "API_KEY"},
		"another link":        {Project: bound.Project, Class: bound.Class, Env: bound.Env, Folder: bound.Folder, Link: "db", Name: bound.Name},
	} {
		if _, code := sealHelperAt(t, root, sealed, append([]string{"open"}, sealFlags(moved)...)...); code == 0 {
			t.Errorf("a value sealed here opened at %s, so the coordinate authenticates nothing", name)
		}
	}
}

func TestTheSealHelperOpensNothingWhoseBytesMoved(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	sealed, code := sealHelperAt(t, root, encoded("postgres://example"), append([]string{"seal"}, aCoordinate...)...)
	if code != 0 {
		t.Fatalf("seal exited %d", code)
	}

	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sealed))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff
	tampered := base64.StdEncoding.EncodeToString(raw)
	if _, code := sealHelperAt(t, root, tampered, append([]string{"open"}, aCoordinate...)...); code == 0 {
		t.Fatal("a sealed value whose bytes moved opened anyway")
	}
}

func TestWhatTheSealHelperWritesIsAES256GCMOverTheKeyOnDisk(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	plaintext := "postgres://example"
	rendered, code := sealHelperAt(t, root, encoded(plaintext), append([]string{"seal"}, aCoordinate...)...)
	if code != 0 {
		t.Fatalf("seal exited %d", code)
	}
	sealed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rendered))
	if err != nil {
		t.Fatal(err)
	}

	key, err := os.ReadFile(filepath.Join(root, sealClass, "seal.key"))
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
	opened, err := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], bound.Binding())
	if err != nil {
		t.Fatalf("what the helper sealed does not open as %s over the key it minted: %v", SealAlgorithm, err)
	}
	if string(opened) != plaintext {
		t.Errorf("what the helper sealed opens as %q, want %q", opened, plaintext)
	}
}

func TestTheSealKeyIsRootsAloneAndIsWrittenAfterTheHelperThatMintsIt(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	items := Items(class, []byte(aKey+"\n"))

	key := written(items, SealKeyPath(class))
	if key.Kind == "" {
		t.Fatalf("nothing in the item set mints %s, so a bootstrapped host seals nothing", SealKeyPath(class))
	}
	if key.Mode != sealKeyMode || key.Owner != rootOwner {
		t.Errorf("%s is written %04o to %q, want %04o to %s: the deploy login opens values, it does not hold the key",
			key.Name, key.Mode, key.Owner, sealKeyMode, rootOwner)
	}
	if len(key.Content) != 0 {
		t.Error("the seal key is written with content from this machine, and a key that leaves the host is no key sealed to it")
	}

	helper := written(items, SealHelper)
	if helper.Kind != KindFile || helper.Owner != rootOwner || helper.Mode&0o022 != 0 {
		t.Errorf("%s is written %04o to %q, and a helper the caller can rewrite is a key the caller can read", SealHelper, helper.Mode, helper.Owner)
	}
	if at(items, SealHelper) > at(items, key.Name) {
		t.Error("the seal key is minted before the helper that mints it exists")
	}
}

func TestTheDeployLoginIsWhitelistedOnTheHelperAndOnNothingBeside(t *testing.T) {
	t.Parallel()

	items := Items(providerkit.ClassProduction, []byte(aKey+"\n"))

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
	if want := deployUser + " ALL=(root) NOPASSWD: " + SealHelper; written != want {
		t.Errorf("the fragment reads %q, want %q: one helper, and no path beside it", written, want)
	}
	if at(items, principal().Name) > at(items, fragment.Name) {
		t.Error("the sudoers line is written before the login it names exists")
	}
}

func TestTheSurveyReadsTheKeysFingerprintWithoutReadingTheKey(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	fingerprint := "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	rendered := KindSealKey + "\t" + SealKeyPath(class) + "\t400\troot\t" + fingerprint + "\t2026-08-26T09:00:00Z\n"

	observed, held, err := readSurvey(rendered)
	if err != nil {
		t.Fatalf("readSurvey() over the row a host answers with = %v", err)
	}
	if held.Fingerprint != fingerprint {
		t.Errorf("the survey read the fingerprint %q, want %q", held.Fingerprint, fingerprint)
	}
	if held.CreatedAt != "2026-08-26T09:00:00Z" {
		t.Errorf("the survey read %q as when the key came into being", held.CreatedAt)
	}
	if got := observed[sealKey(class).ID()]; got != sealKey(class).Digest() {
		t.Errorf("a key standing as ocel minted it surveys as %q, want %q: the bytes of a key are never what says it is current", got, sealKey(class).Digest())
	}
}

func TestTheSurveyTheHostRunsAnswersForAKeyThatStandsAndSaysNothingForOneThatDoesNot(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	key := filepath.Join(root, sealClass, "seal.key")
	item := Item{Kind: KindSealKey, Name: key, Mode: sealKeyMode, Owner: owning(t), Class: providerkit.ClassProduction}

	if rendered := sh(t, t.TempDir(), sealSurvey(item)); strings.TrimSpace(rendered) != "" {
		t.Fatalf("the survey answered %q where no key stands", rendered)
	}

	if _, code := sealHelperAt(t, root, "", "init"); code != 0 {
		t.Fatalf("init exited %d", code)
	}
	observed, held, err := readSurvey(sh(t, t.TempDir(), sealSurvey(item)))
	if err != nil {
		t.Fatalf("readSurvey() over what the survey script answers = %v", err)
	}
	if len(held.Fingerprint) != 64 {
		t.Errorf("the survey read %q as the key's fingerprint, want a SHA256", held.Fingerprint)
	}
	if held.Algorithm != SealAlgorithm {
		t.Errorf("the survey read the algorithm %q, want %q", held.Algorithm, SealAlgorithm)
	}
	if !strings.HasSuffix(held.CreatedAt, "Z") {
		t.Errorf("the survey read %q as when the key came into being, want a UTC instant", held.CreatedAt)
	}
	if got := observed[item.ID()]; got != item.Digest() {
		t.Errorf("a key standing as ocel minted it surveys as %q, want %q", got, item.Digest())
	}
}

func TestWritingAKeyThatStandsReassertsItsPostureAndMintsNothing(t *testing.T) {
	t.Parallel()

	root := sealDir(t)
	key := filepath.Join(root, sealClass, "seal.key")
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
		if err := os.WriteFile(filepath.Join(stubbed, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	item := Item{Kind: KindSealKey, Name: key, Mode: sealKeyMode, Owner: rootOwner, Class: providerkit.ClassProduction}
	sh(t, stubbed, item.command())

	log := ran(t, stubbed)
	for _, want := range []string{"chown root:root " + key, "chmod 0400 " + key} {
		if !strings.Contains(log, want) {
			t.Errorf("writing a key that stands ran\n%s\nwant it to run %q", log, want)
		}
	}
	again, err := os.ReadFile(key)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(minted) {
		t.Error("writing a key that stands minted a new one, and every value sealed to the old one went with it")
	}
}

func owning(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(sh(t, t.TempDir(), "id -un"))
}

func TestAReplacedKeyIsDriftThoughEveryPathStillStandsAsItWasWritten(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	keys := []byte(aKey + "\n")
	observed := digests(Items(class, keys))
	minted := Seal{Fingerprint: "9f86d081884c7d659a", Algorithm: SealAlgorithm, CreatedAt: "2026-08-26T09:00:00Z"}

	read := Reading{
		Class: class, Keys: keys, Present: true, Observed: observed, Seal: minted,
		Stamp: Stamp{State: StateComplete, Digests: observed, Seal: minted},
	}
	if !read.settled() {
		t.Fatal("a host standing exactly as it was applied reads as drifted")
	}

	read.Seal = Seal{Fingerprint: "0000000000000000", Algorithm: SealAlgorithm, CreatedAt: "2026-08-26T10:00:00Z"}
	if read.settled() {
		t.Error("a host whose seal key was replaced reads as settled, so drift in what every secret opens to is invisible")
	}
}

func TestDestroyNamesTheKeyAsDataBearingAndKeepsTheHelperWhileASiblingStands(t *testing.T) {
	t.Parallel()

	production, preview := providerkit.ClassProduction, providerkit.ClassPreview
	keys := []byte(aKey + "\n")
	held := digests(Items(production, keys))

	alone := removing(Reading{Class: production, Keys: keys, Observed: held}, Reading{Class: preview, Observed: map[string]string{}})
	key := removalOf(alone, SealKeyPath(production))
	if key.path == "" {
		t.Fatalf("destroy leaves %s behind, and a key nothing takes is every sealed value still openable", SealKeyPath(production))
	}
	if key.reason == "" {
		t.Error("destroy takes the seal key with no reason, and the typed confirmation must name what is unrecoverable")
	}
	if index(alone, key.path) > index(alone, ClassDir(production)) {
		t.Error("the class directory is removed before the key it carries is named, so the confirmation names bytes that are already gone")
	}
	for _, singleton := range []string{SealHelper, sudoersSeal} {
		if removalOf(alone, singleton).path == "" {
			t.Errorf("destroying the last class leaves %s behind", singleton)
		}
	}

	beside := digests(Items(preview, keys))
	shared := removing(Reading{Class: production, Keys: keys, Observed: held}, Reading{Class: preview, Keys: keys, Observed: beside})
	for _, singleton := range []string{SealHelper, sudoersSeal} {
		if removalOf(shared, singleton).path != "" {
			t.Errorf("destroying one class takes %s, which a standing sibling still seals through", singleton)
		}
	}
	if removalOf(shared, SealKeyPath(production)).path == "" {
		t.Error("destroying one class leaves its own key behind, and a class is what a key is scoped to")
	}
}

func TestTheProviderReachesTheKeyOnlyThroughTheHelperItInstalled(t *testing.T) {
	t.Parallel()

	at := providerkit.Coordinate{
		Project: "shop",
		Class:   providerkit.ClassProduction,
		Env:     "*",
		Folder:  "/apps/web",
		Link:    "db",
		Name:    "DATABASE_URL",
	}
	argv, err := sealArgv("open", at)
	if err != nil {
		t.Fatalf("sealArgv() = %v", err)
	}
	if argv[0] != SealHelper {
		t.Errorf("the provider runs %q, want the helper it installed and nothing beside", argv[0])
	}
	command := words(argv)
	if strings.Contains(command, SealKeyPath(at.Class)) {
		t.Errorf("the provider names the key on the command line: %q", command)
	}
	if argv[1] != string(at.Class) || argv[2] != "open" {
		t.Errorf("the provider runs %q, want the class it seals under and the verb it asks for", argv)
	}
	for flag, want := range map[string]string{
		"--project": at.Project,
		"--env":     at.Env,
		"--folder":  at.Folder,
		"--link":    at.Link,
		"--name":    at.Name,
	} {
		if handed(argv, flag) != want {
			t.Errorf("the provider runs %q, which hands %s %q, so the coordinate would authenticate less than it names",
				argv, flag, handed(argv, flag))
		}
	}
}

func handed(argv []string, flag string) string {
	for i, arg := range argv {
		if arg == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

func TestTheHelperIsRunInTheShapeTheSudoersLineWhitelists(t *testing.T) {
	t.Parallel()

	argv, err := sealArgv("seal", providerkit.Coordinate{
		Project: "shop", Class: providerkit.ClassProduction, Env: "*", Folder: "/", Name: "DATABASE_URL",
	})
	if err != nil {
		t.Fatalf("sealArgv() = %v", err)
	}

	ran := "sudo -n " + words(argv)
	if strings.Contains(ran, "sh -c") {
		t.Fatalf("the deploy login runs %q, and the line in %s whitelists %s, not a shell", ran, sudoersSeal, SealHelper)
	}
	if want := "sudo -n " + quoted(SealHelper) + " "; !strings.HasPrefix(ran, want) {
		t.Errorf("the deploy login runs %q, want it to begin %q: sudo matches the command it is handed, and nothing else runs", ran, want)
	}
}

func TestAValueSealedToNoClassIsRefusedRatherThanSealedToWhateverStands(t *testing.T) {
	t.Parallel()

	_, err := sealArgv("seal", providerkit.Coordinate{Project: "shop", Name: "DATABASE_URL"})
	var refusal providerkit.Refusal
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("sealing at a coordinate naming no class = %v, want a refusal: a key is minted per class", err)
	}
}

func TestACoordinateMissingAnyPartButTheLinkIsRefused(t *testing.T) {
	t.Parallel()

	whole := providerkit.Coordinate{
		Project: "shop", Class: providerkit.ClassProduction, Env: "*", Folder: "/", Link: "db", Name: "DATABASE_URL",
	}
	for name, blanked := range map[string]func(*providerkit.Coordinate){
		"project": func(at *providerkit.Coordinate) { at.Project = "" },
		"env":     func(at *providerkit.Coordinate) { at.Env = "" },
		"folder":  func(at *providerkit.Coordinate) { at.Folder = "" },
		"name":    func(at *providerkit.Coordinate) { at.Name = "" },
	} {
		at := whole
		blanked(&at)
		_, err := sealArgv("seal", at)
		var refusal providerkit.Refusal
		if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
			t.Errorf("sealing at a coordinate naming no %s = %v, want a refusal: the coordinate is what a value is bound to", name, err)
		}
	}

	at := whole
	at.Link = ""
	if _, err := sealArgv("seal", at); err != nil {
		t.Errorf("sealing a value that belongs to no link = %v, want the seal every plain value takes", err)
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
