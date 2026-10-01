package host

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/boxstore"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestTheKeyValueHelperComparesAndSetsUnderItsOwnLock(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	name := "conformance+compared/compared"

	first := helperWrite(t, dir, name, "", "one")
	if first == "" {
		t.Fatal("the helper wrote an entry and reported no revision, and a compare-and-set has nothing to compare")
	}
	if revision, body := helperRead(t, dir, name); revision != first || body != "one" {
		t.Fatalf("read back %q at %q, want %q at %q", body, revision, "one", first)
	}

	if _, code := helper(t, dir, "", "write", name, ""); code != boxstore.ExitStale {
		t.Errorf("a write at a taken name naming no revision exited %d, want %d", code, boxstore.ExitStale)
	}
	second := helperWrite(t, dir, name, first, "two")
	if _, code := helper(t, dir, "", "write", name, first); code != boxstore.ExitStale {
		t.Errorf("a second write at a revision that moved exited %d, want %d", code, boxstore.ExitStale)
	}

	if _, code := helper(t, dir, "", "remove", name, first); code != boxstore.ExitStale {
		t.Errorf("a removal at a revision that moved exited %d, want %d", code, boxstore.ExitStale)
	}
	if _, code := helper(t, dir, "", "remove", name, second); code != 0 {
		t.Errorf("a removal at the current revision exited %d, want it gone", code)
	}
	if _, code := helper(t, dir, "", "read", name); code != boxstore.ExitNotFound {
		t.Errorf("a read after a removal exited %d, want %d", code, boxstore.ExitNotFound)
	}
}

func TestTheKeyValueHelperRefusesAPairWhereEitherHalfMoved(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	one, two := "conformance+pair/record", "conformance+pair/value"

	fed := asJSON("one") + "\n" + asJSON("one") + "\n"
	if _, code := helper(t, dir, fed, "pair", one, "", two, ""); code != 0 {
		t.Fatalf("a pair of new entries exited %d, want both stored", code)
	}
	current, _ := helperRead(t, dir, one)
	moved := "a revision nobody wrote"
	if _, code := helper(t, dir, asJSON("two")+"\n"+asJSON("two")+"\n", "pair", one, current, two, moved); code != boxstore.ExitStale {
		t.Fatalf("a pair where one half moved exited %d, want %d", code, boxstore.ExitStale)
	}
	for _, name := range []string{one, two} {
		if _, body := helperRead(t, dir, name); body != "one" {
			t.Errorf("%s reads %q after a refused pair write, want the bytes from the write that landed", name, body)
		}
	}
}

func TestTheKeyValueHelperListsEverythingUnderAPrefixAndNothingBeside(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	for _, name := range []string{
		"conformance+tree/tree",
		"conformance+tree/tree/a",
		"conformance+tree/tree/b/one",
		"conformance+tree/tree/b/two",
		"conformance+tree/treeish",
		"conformance+tree+tree/a",
	} {
		helperWrite(t, dir, name, "", name)
	}

	for prefix, want := range map[string]int{
		"tree":   4,
		"tree/b": 2,
		"":       5,
		"absent": 0,
	} {
		args := []string{"list", "conformance+tree"}
		if prefix != "" {
			args = append(args, prefix)
		}
		rendered, code := helper(t, dir, "", args...)
		if code != 0 {
			t.Fatalf("list %q exited %d", prefix, code)
		}
		if got := rows(rendered); got != want {
			t.Errorf("list %q returned %d rows, want %d", prefix, got, want)
		}
	}
}

const helperTier = "production"

func helperDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("no flock on this machine, and the helper takes its lock with it")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, helperTier, "keyvalues"), 0o750); err != nil {
		t.Fatal(err)
	}
	return root
}

func keyValuesDir(t *testing.T, root string) string {
	t.Helper()
	return filepath.Join(root, helperTier, "keyvalues")
}

func helper(t *testing.T, root, stdin string, args ...string) (string, int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "keyvalues")
	if err := os.WriteFile(script, keyValuesScript, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", append([]string{script, helperTier}, args...)...)
	cmd.Env = append(os.Environ(), "OCEL_STATE_ROOT="+root)
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
		t.Fatalf("run the key-value helper: %v\n%s", err, stderr.String())
		return "", 0
	}
}

func rows(rendered string) int {
	trimmed := strings.TrimSpace(rendered)
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}

func helperWrite(t *testing.T, dir, name, expected, body string) string {
	t.Helper()
	rendered, code := helper(t, dir, asJSON(body)+"\n", "write", name, expected)
	if code != 0 {
		t.Fatalf("write %s exited %d", name, code)
	}
	return strings.TrimSpace(rendered)
}

func helperRead(t *testing.T, dir, name string) (string, string) {
	t.Helper()
	rendered, code := helper(t, dir, "", "read", name)
	if code != 0 {
		t.Fatalf("read %s exited %d", name, code)
	}
	revision, body, _ := strings.Cut(strings.TrimRight(rendered, "\n"), "\t")
	var decoded string
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("the helper answered %q, which no entry was written as", body)
	}
	return revision, decoded
}

func asJSON(body string) string {
	value, _ := json.Marshal(body)
	return string(value)
}

func encoded(body string) string {
	return base64.StdEncoding.EncodeToString([]byte(body))
}

func TestAPairGivenOneBodyWritesNeitherHalf(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	one, two := "conformance+pair/record", "conformance+pair/value"

	if _, code := helper(t, dir, asJSON("one")+"\n"+asJSON("one")+"\n", "pair", one, "", two, ""); code != 0 {
		t.Fatalf("a pair of new entries exited %d, want both stored", code)
	}
	first, _ := helperRead(t, dir, one)
	second, _ := helperRead(t, dir, two)

	if _, code := helper(t, dir, asJSON("two")+"\n", "pair", one, first, two, second); code == 0 {
		t.Fatal("a pair fed one body exited 0, and the half it was never given was written away")
	}
	for _, name := range []string{one, two} {
		if _, body := helperRead(t, dir, name); body != "one" {
			t.Errorf("%s reads %q after a pair fed one body, want the bytes that were there before it", name, body)
		}
	}
}

func TestAnEntryThatNamesNoRevisionIsNotOverwritten(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	name := "conformance+truncated/truncated"
	helperWrite(t, dir, name, "", "one")

	f := filepath.Join(keyValuesDir(t, dir), name+".json")
	if err := os.Truncate(f, 0); err != nil {
		t.Fatal(err)
	}
	if _, code := helper(t, dir, asJSON("two")+"\n", "write", name, ""); code == 0 {
		t.Fatal("a write over an entry with no revision exited 0, and a compare-and-set that compares nothing is a lost update")
	}
}

func TestAnEntryIsReadableOnlyByTheUserThatWroteIt(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	name := "values+shop/cells/DATABASE_URL"
	helperWrite(t, dir, name, "", "postgres://example")

	f := filepath.Join(keyValuesDir(t, dir), name+".json")
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Errorf("%s has mode %04o, and an entry contains the values of a deploy", f, info.Mode().Perm())
	}
	within, err := os.Stat(filepath.Dir(f))
	if err != nil {
		t.Fatal(err)
	}
	if within.Mode().Perm()&0o077 != 0 {
		t.Errorf("%s has mode %04o, and the names beneath it are a deploy's alone", filepath.Dir(f), within.Mode().Perm())
	}
}

func TestAKeyValueDirectoryThatIsASymlinkIsRefusedAndNothingLandsWhereItPoints(t *testing.T) {
	t.Parallel()

	root := helperDir(t)
	elsewhere := t.TempDir()
	dir := keyValuesDir(t, root)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Fatal(err)
	}

	for _, args := range [][]string{{"list", "conformance+a"}, {"read", "conformance+a/a"}} {
		if rendered, code := helper(t, root, "", args...); code == 0 {
			t.Errorf("%s through a key-value directory that points at %s exited 0 with %q, want it refused", args[0], elsewhere, rendered)
		}
	}
	if _, code := helper(t, root, asJSON("one")+"\n", "write", "conformance+a/a", ""); code == 0 {
		t.Error("a write through a key-value directory that is a symlink exited 0, want it refused")
	}
	left, err := os.ReadDir(elsewhere)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("%s holds %d entries after the helper ran through a symlink to it, want nothing: whoever can swap the key-value directory would choose where root writes", elsewhere, len(left))
	}
}

func TestAListingThatCannotBeReadIsNoEmptyListing(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root reads every directory, so nothing here can be made unreadable")
	}
	dir := helperDir(t)
	helperWrite(t, dir, "conformance+tree/tree/b/one", "", "one")

	shut := filepath.Join(keyValuesDir(t, dir), "conformance+tree/tree/b")
	if err := os.Chmod(shut, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shut, 0o700) })

	rendered, code := helper(t, dir, "", "list", "conformance+tree", "tree")
	if code == 0 {
		t.Fatalf("a listing over a directory nothing can read exited 0 with %q, and a reconciler reads that as a prefix that is empty", rendered)
	}
}

func TestAKeyValueTierTheLoginCannotReachIsNotCalledMissing(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root enters every directory, so nothing here can be made unreachable")
	}
	dir := helperDir(t)
	shut := filepath.Join(dir, helperTier)
	if err := os.Chmod(shut, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shut, 0o750) })

	said := unreachable(t, keyValuesScript, "OCEL_STATE_ROOT="+dir, helperTier, "list", "conformance+tree", "tree")
	if strings.Contains(said, "missing") || !strings.Contains(said, shut+": Permission denied") {
		t.Errorf("the helper said %q, want %s named as denied rather than a missing tier to bootstrap again", said, shut)
	}
}

func TestTheKeyValueTierIsReachedUnderNoElevationAtAll(t *testing.T) {
	t.Parallel()

	b := machine(nil)
	b.facts = session.Facts{Systemd: true}
	b.floor = refusal.Refuse(refusal.CodeDenied,
		"%s can neither act as root nor run sudo without a password", deployUser)
	b.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "echo present"):
			return session.Result{Stdout: "present\n"}, true
		case strings.Contains(command, boxstore.KeyValuesHelper):
			return session.Result{Stdout: "0123456789abcdef0123456789abcdef\t{}\n"}, true
		}
		return session.Result{}, false
	}

	entry, err := NewKeyValues(b.host()).Read(context.Background(), stackrecords.ProjectKey(environment.TierProduction, "shop"))
	if err != nil {
		t.Fatalf("Read() as the login every deploy runs as = %v", err)
	}
	if string(entry.Value) != "{}" {
		t.Errorf("the read answered %q, want the row the helper rendered", entry.Value)
	}
	reached := 0
	for _, command := range b.commands() {
		if !strings.Contains(command, boxstore.KeyValuesHelper) {
			continue
		}
		reached++
		if strings.Contains(command, "sudo") {
			t.Errorf("the read ran as %q: %s has no sudoers line beside the seal helper, so a key-value tier reached through sudo is one no deploy ever reads", command, deployUser)
		}
	}
	if reached == 0 {
		t.Error("no command the read ran named the helper at all, so this test read nothing")
	}
}

func TestAnEntryThisLoginCannotWriteNamesTheElevationItWasRefused(t *testing.T) {
	t.Parallel()

	b := machine(nil)
	b.facts = session.Facts{Systemd: true}
	b.floor = refusal.Refuse(refusal.CodeDenied,
		"%s can neither act as root nor run sudo without a password", deployUser)
	b.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "echo present"):
			return session.Result{Stdout: "present\n"}, true
		case strings.Contains(command, boxstore.KeyValuesHelper):
			return session.Result{Code: 1, Stderr: "Permission denied"}, true
		}
		return session.Result{}, false
	}

	_, err := NewKeyValues(b.host()).Read(context.Background(), stackrecords.ProjectKey(environment.TierProduction, "shop"))
	if err == nil {
		t.Fatal("a key-value tier this login could neither read nor elevate to read answered a row")
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("the read failed with\n%s\nand never names the elevation that was refused; the deploy login owns this tier, so a permission error on it is the preflight refusal showing up somewhere it cannot be acted on", err)
	}
}
