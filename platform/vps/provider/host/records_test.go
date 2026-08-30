package host

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestARecordNameSurvivesTheNameAFileOnTheHostAnswersTo(t *testing.T) {
	t.Parallel()

	for _, name := range []providerkit.RecordName{
		{"conformance", "production", "TestOne/sub", "leaf"},
		{"values", "shop", "production", "/apps/web", "DATABASE_URL"},
		{"ledger", "production/shop", ".hidden", ".."},
	} {
		encoded, err := encode(name)
		if err != nil {
			t.Fatalf("encode(%s) = %v", name, err)
		}
		if strings.Contains(encoded, "/.") {
			t.Errorf("encode(%s) = %q, and a segment that starts a dot names something no record is", name, encoded)
		}
		decoded, err := decode(encoded)
		if err != nil {
			t.Fatalf("decode(%q) = %v", encoded, err)
		}
		if !reflect.DeepEqual(decoded, name) {
			t.Errorf("decode(encode(%s)) = %s", name, decoded)
		}
	}
}

func TestARecordNameWithAnEmptySegmentIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := encode(providerkit.RecordName{"conformance", "", "leaf"}); err == nil {
		t.Fatal("encode() of a name with an empty segment succeeded, and no file on a host answers to it")
	}
}

func TestTheRecordsHelperComparesAndSetsUnderItsOwnLock(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	name := "conformance/production/held"

	first := helperWrite(t, dir, name, "", "one")
	if first == "" {
		t.Fatal("the helper wrote a record and reported no revision, and a compare-and-set has nothing to compare")
	}
	if revision, body := helperRead(t, dir, name); revision != first || body != "one" {
		t.Fatalf("read back %q at %q, want %q at %q", body, revision, "one", first)
	}

	if _, code := helper(t, dir, "", "write", name, ""); code != exitStale {
		t.Errorf("a write at a taken name naming no revision exited %d, want %d", code, exitStale)
	}
	second := helperWrite(t, dir, name, first, "two")
	if _, code := helper(t, dir, "", "write", name, first); code != exitStale {
		t.Errorf("a second write at a revision that moved exited %d, want %d", code, exitStale)
	}

	if _, code := helper(t, dir, "", "remove", name, first); code != exitStale {
		t.Errorf("a removal at a revision that moved exited %d, want %d", code, exitStale)
	}
	if _, code := helper(t, dir, "", "remove", name, second); code != 0 {
		t.Errorf("a removal at the revision held exited %d, want it gone", code)
	}
	if _, code := helper(t, dir, "", "read", name); code != exitNoRecord {
		t.Errorf("a read after a removal exited %d, want %d", code, exitNoRecord)
	}
}

func TestTheRecordsHelperRefusesAPairWhereEitherHalfMoved(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	one, two := "conformance/production/pair/record", "conformance/production/pair/value"

	fed := encoded("one") + "\n" + encoded("one") + "\n"
	if _, code := helper(t, dir, fed, "pair", one, "", two, ""); code != 0 {
		t.Fatalf("a pair of new records exited %d, want both stored", code)
	}
	held, _ := helperRead(t, dir, one)
	moved := "a revision nobody wrote"
	if _, code := helper(t, dir, encoded("two")+"\n"+encoded("two")+"\n", "pair", one, held, two, moved); code != exitStale {
		t.Fatalf("a pair where one half moved exited %d, want %d", code, exitStale)
	}
	for _, name := range []string{one, two} {
		if _, body := helperRead(t, dir, name); body != "one" {
			t.Errorf("%s reads %q after a refused pair write, want the bytes from the write that landed", name, body)
		}
	}
}

func TestTheRecordsHelperListsEverythingUnderAPrefixAndNothingBeside(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	for _, name := range []string{
		"conformance/production/tree/a",
		"conformance/production/tree/b/one",
		"conformance/production/tree/b/two",
		"conformance/production/treeish",
	} {
		helperWrite(t, dir, name, "", name)
	}

	for prefix, want := range map[string]int{
		"conformance/production/tree":   3,
		"conformance/production/tree/b": 2,
		"conformance/production":        4,
		"conformance/production/absent": 0,
	} {
		rendered, code := helper(t, dir, "", "list", prefix)
		if code != 0 {
			t.Fatalf("list %q exited %d", prefix, code)
		}
		if got := rows(rendered); got != want {
			t.Errorf("list %q returned %d rows, want %d", prefix, got, want)
		}
	}
}

const helperClass = "production"

func helperDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("no flock on this machine, and the helper takes its lock with it")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, helperClass, "records"), 0o750); err != nil {
		t.Fatal(err)
	}
	return root
}

func records(t *testing.T, root string) string {
	t.Helper()
	return filepath.Join(root, helperClass, "records")
}

func helper(t *testing.T, root, stdin string, args ...string) (string, int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "records")
	if err := os.WriteFile(script, recordsScript, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", append([]string{script, helperClass}, args...)...)
	cmd.Env = append(os.Environ(), "OCEL_RECORDS_ROOT="+root)
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
		t.Fatalf("run the records helper: %v\n%s", err, stderr.String())
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
	rendered, code := helper(t, dir, encoded(body)+"\n", "write", name, expected)
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
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("the helper answered %q, which no record was written as", body)
	}
	return revision, string(decoded)
}

func encoded(body string) string {
	return base64.StdEncoding.EncodeToString([]byte(body))
}

func TestAPairGivenOneBodyWritesNeitherHalf(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	one, two := "conformance/production/pair/record", "conformance/production/pair/value"

	if _, code := helper(t, dir, encoded("one")+"\n"+encoded("one")+"\n", "pair", one, "", two, ""); code != 0 {
		t.Fatalf("a pair of new records exited %d, want both stored", code)
	}
	first, _ := helperRead(t, dir, one)
	second, _ := helperRead(t, dir, two)

	if _, code := helper(t, dir, encoded("two")+"\n", "pair", one, first, two, second); code == 0 {
		t.Fatal("a pair fed one body exited 0, and the half it was never given was written away")
	}
	for _, name := range []string{one, two} {
		if _, body := helperRead(t, dir, name); body != "one" {
			t.Errorf("%s reads %q after a pair fed one body, want the bytes that stood before it", name, body)
		}
	}
}

func TestARecordThatNamesNoRevisionIsNotOverwritten(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	name := "conformance/production/truncated"
	helperWrite(t, dir, name, "", "one")

	f := filepath.Join(records(t, dir), name+".rec")
	if err := os.Truncate(f, 0); err != nil {
		t.Fatal(err)
	}
	if _, code := helper(t, dir, encoded("two")+"\n", "write", name, ""); code == 0 {
		t.Fatal("a write over a record holding no revision exited 0, and a compare-and-set that compares nothing is a lost update")
	}
}

func TestARecordIsReadableOnlyByTheUserThatWroteIt(t *testing.T) {
	t.Parallel()

	dir := helperDir(t)
	name := "conformance/production/values/DATABASE_URL"
	helperWrite(t, dir, name, "", "postgres://example")

	f := filepath.Join(records(t, dir), name+".rec")
	held, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	if held.Mode().Perm()&0o077 != 0 {
		t.Errorf("%s stands at %04o, and a record carries the values of a deploy", f, held.Mode().Perm())
	}
	within, err := os.Stat(filepath.Dir(f))
	if err != nil {
		t.Fatal(err)
	}
	if within.Mode().Perm()&0o077 != 0 {
		t.Errorf("%s stands at %04o, and the names beneath it are a deploy's alone", filepath.Dir(f), within.Mode().Perm())
	}
}

func TestAListingThatCannotBeReadIsNoEmptyListing(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root reads every directory, so nothing here can be made unreadable")
	}
	dir := helperDir(t)
	helperWrite(t, dir, "conformance/production/tree/b/one", "", "one")

	shut := filepath.Join(records(t, dir), "conformance/production/tree/b")
	if err := os.Chmod(shut, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shut, 0o700) })

	rendered, code := helper(t, dir, "", "list", "conformance/production/tree")
	if code == 0 {
		t.Fatalf("a listing over a directory nothing can read exited 0 with %q, and a reconciler reads that as a prefix that is empty", rendered)
	}
}

func TestTheRecordTierIsReachedUnderNoElevationAtAll(t *testing.T) {
	t.Parallel()

	b := machine(nil)
	b.facts = session.Facts{Systemd: true}
	b.floor = providerkit.Refuse(providerkit.CodeDenied,
		"%s can neither act as root nor run sudo without a password", deployUser)
	b.answer = func(command string) (session.Result, bool) {
		switch {
		case strings.Contains(command, "echo held"):
			return session.Result{Stdout: "held\n"}, true
		case strings.Contains(command, recordsHelper):
			return session.Result{Stdout: "0123456789abcdef0123456789abcdef\t" + base64.StdEncoding.EncodeToString([]byte("{}")) + "\n"}, true
		}
		return session.Result{}, false
	}

	held, err := NewRecords(b.host()).Read(context.Background(), providerkit.ProjectRecord(providerkit.ClassProduction, "shop"))
	if err != nil {
		t.Fatalf("Read() as the login every deploy runs as = %v", err)
	}
	if string(held.Bytes) != "{}" {
		t.Errorf("the read answered %q, want the row the helper rendered", held.Bytes)
	}
	reached := 0
	for _, command := range b.commands() {
		if !strings.Contains(command, recordsHelper) {
			continue
		}
		reached++
		if strings.Contains(command, "sudo") {
			t.Errorf("the read ran as %q: %s holds no sudoers line beside the seal helper, so a record tier reached through sudo is a record tier no deploy ever reads", command, deployUser)
		}
	}
	if reached == 0 {
		t.Error("no command the read ran named the helper at all, so this test read nothing")
	}
}
