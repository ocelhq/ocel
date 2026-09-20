package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type backupBench struct {
	root string
	bin  string
}

func backupsOn(t *testing.T, labelled ...string) backupBench {
	t.Helper()
	bench := backupBench{root: t.TempDir(), bin: t.TempDir()}
	docker := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >>" + filepath.Join(bench.bin, "log") + "\n" +
		"case \"$1\" in\n" +
		"ps) cat " + filepath.Join(bench.bin, "labelled") + " ;;\n" +
		"inspect) printf '%s\\n' \"$(cat " + filepath.Join(bench.bin, "mounted") + ")\" ;;\n" +
		"exec)\n" +
		"  for arg in \"$@\"; do case $arg in\n" +
		"    pg_dump) if [ -f " + filepath.Join(bench.bin, "broken") + " ]; then printf 'half a du'; echo 'pg_dump: connection refused' >&2; exit 1; fi\n" +
		"             printf 'dump of %s' \"$2\"; exit 0 ;;\n" +
		"    pg_dumpall) printf 'roles of %s' \"$2\"; exit 0 ;;\n" +
		"    pg_restore) cat >" + filepath.Join(bench.bin, "restored") + "; exit 0 ;;\n" +
		"    psql) cat >" + filepath.Join(bench.bin, "restored-roles") + "; exit 0 ;;\n" +
		"  esac; done ;;\n" +
		"esac\n"
	df := "#!/bin/sh\nprintf 'Avail\\n%s\\n' \"$(cat " + filepath.Join(bench.bin, "free") + ")\"\n"
	for name, body := range map[string]string{"docker": docker, "df": df} {
		if err := os.WriteFile(filepath.Join(bench.bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bench.holds(t, "labelled", strings.Join(labelled, "\n")+"\n")
	bench.holds(t, "free", "1000000000")
	mounted := filepath.Join(bench.root, "volume")
	if err := os.MkdirAll(mounted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mounted, "object"), []byte("bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	bench.holds(t, "mounted", mounted)
	return bench
}

func (b backupBench) holds(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(b.bin, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (b backupBench) runs(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	script := filepath.Join(b.bin, "backups")
	if err := os.WriteFile(script, backupsScript, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), "OCEL_BACKUPS_ROOT="+b.root, "PATH="+b.bin+":"+os.Getenv("PATH"))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return strings.TrimSpace(stdout.String()), stderr.String(), code
}

func (b backupBench) kept(t *testing.T, class, container string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(b.root, class, "backups", container))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

func TestADumpLandsWholeUnderTheContainerItCameFromAndSaysWhere(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	said, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg", "main")
	if code != 0 {
		t.Fatalf("dump exited %d: %s", code, stderr)
	}
	kept := bench.kept(t, "production", "shop-main-pg")
	if len(kept) != 2 || !strings.HasSuffix(kept[0], ".dump") || !strings.HasSuffix(kept[1], ".roles.sql") {
		t.Fatalf("the backups hold %v, want the one dump, the roles it was taken with, and nothing half-written beside them", kept)
	}
	if roles, _ := os.ReadFile(filepath.Join(bench.root, "production", "backups", "shop-main-pg", kept[1])); string(roles) != "roles of shop-main-pg" {
		t.Errorf("the roles kept beside the dump are %q, and a database restored without the roles it grants to does not restore", roles)
	}
	if said != filepath.Join(bench.root, "production", "backups", "shop-main-pg", kept[0]) {
		t.Errorf("dump said %q, and an upgrade restores from the path it is told", said)
	}
	body, err := os.ReadFile(said)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "dump of shop-main-pg" {
		t.Errorf("the dump holds %q, want what pg_dump wrote", body)
	}
}

func TestADumpThatFailsLeavesNothingBehindAndKeepsWhatWasThere(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	if _, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg", "main"); code != 0 {
		t.Fatalf("the first dump exited %d: %s", code, stderr)
	}
	before := bench.kept(t, "production", "shop-main-pg")
	bench.holds(t, "broken", "")
	_, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg", "main")
	if code == 0 {
		t.Fatal("a pg_dump that failed was reported as a backup taken")
	}
	if !strings.Contains(stderr, "connection refused") {
		t.Errorf("the failure says %q and never why", stderr)
	}
	if after := bench.kept(t, "production", "shop-main-pg"); !slices.Equal(after, before) {
		t.Errorf("the backups went from %v to %v, and half a dump restores to half a database", before, after)
	}
}

func TestOnlyTheNewestDumpsAreKept(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	dir := filepath.Join(bench.root, "production", "backups", "shop-main-pg")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for day := 1; day <= 9; day++ {
		name := fmt.Sprintf("2026090%dT000000Z.dump", day)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg", "main"); code != 0 {
		t.Fatalf("dump exited %d: %s", code, stderr)
	}
	kept := slices.DeleteFunc(bench.kept(t, "production", "shop-main-pg"), func(name string) bool { return !strings.HasSuffix(name, ".dump") })
	if len(kept) != 7 {
		t.Fatalf("the backups hold %d dumps, want the newest 7: %v", len(kept), kept)
	}
	if all := bench.kept(t, "production", "shop-main-pg"); len(all) != 8 {
		t.Errorf("the backups hold %v, want 7 dumps and the roles of the one just taken: roles whose dump is gone are bytes nothing reads", all)
	}
	if slices.Contains(kept, "20260901T000000Z.dump") || !slices.Contains(kept, "20260909T000000Z.dump") {
		t.Errorf("the backups kept %v, and the ones to go are the oldest", kept)
	}
}

func TestADumpIsRefusedWhenTheDiskCannotHoldAnotherOne(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	dir := filepath.Join(bench.root, "production", "backups", "shop-main-pg")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20260901T000000Z.dump"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	bench.holds(t, "free", "4096")
	_, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg", "main")
	if code == 0 {
		t.Fatal("a dump was taken onto a disk with room for less than two of the last one, and a full disk takes the database down with it")
	}
	if !strings.Contains(stderr, "free") {
		t.Errorf("the refusal says %q and never what is short", stderr)
	}
}

func TestASweepDumpsEveryResourceThatAsksAndOneFailureStopsNoOther(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t,
		"shop-main-pg\tproduction\tmain\tpg",
		"blog-posts-pg\tpreview\tposts\tpg",
		"shop-store-s3\tproduction\tstore\tvol",
		"shop-web-app\tproduction\tweb\t",
	)
	if _, stderr, code := bench.runs(t, "sweep"); code != 0 {
		t.Fatalf("sweep exited %d: %s", code, stderr)
	}
	if got := bench.kept(t, "production", "shop-main-pg"); len(got) != 2 {
		t.Errorf("shop's postgres has %v", got)
	}
	if got := bench.kept(t, "preview", "blog-posts-pg"); len(got) != 2 {
		t.Errorf("blog's postgres has %v", got)
	}
	store := bench.kept(t, "production", "shop-store-s3")
	if len(store) != 1 || !strings.HasSuffix(store[0], ".tar") {
		t.Errorf("shop's store has %v, want a tar of the volume it mounts", store)
	}
	if got := bench.kept(t, "production", "shop-web-app"); len(got) != 0 {
		t.Errorf("a container that asks for no dump has %v", got)
	}
}

func (b backupBench) drove(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.bin, "log"))
	if err != nil {
		t.Fatalf("nothing drove the engine: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestAVolumeIsCopiedWhileNothingIsWritingToIt(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t, "shop-store-s3\tproduction\tstore\tvol")
	if _, stderr, code := bench.runs(t, "sweep"); code != 0 {
		t.Fatalf("sweep exited %d: %s", code, stderr)
	}
	drove := bench.drove(t)
	paused := slices.Index(drove, "pause shop-store-s3")
	unpaused := slices.Index(drove, "unpause shop-store-s3")
	if paused < 0 || unpaused < 0 {
		t.Fatalf("the store kept serving while its volume was copied, so the tar holds a half-written object:\n%s",
			strings.Join(drove, "\n"))
	}
	if unpaused < paused {
		t.Errorf("the store was unpaused before it was paused:\n%s", strings.Join(drove, "\n"))
	}
}

func TestAVolumeCopyThatFailsStillStartsTheStoreAgain(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t, "shop-store-s3\tproduction\tstore\tvol")
	bench.holds(t, "mounted", filepath.Join(bench.root, "nothing-mounted-here"))
	if _, _, code := bench.runs(t, "sweep"); code == 0 {
		t.Fatal("a tar of a directory that is not there was reported as a backup")
	}
	if !slices.Contains(bench.drove(t), "unpause shop-store-s3") {
		t.Fatalf("a failed copy left the store paused, and a paused store answers nothing:\n%s",
			strings.Join(bench.drove(t), "\n"))
	}
}

func TestARestoreFeedsTheDumpBackThroughTheContainer(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	path, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg", "main")
	if code != 0 {
		t.Fatalf("dump exited %d: %s", code, stderr)
	}
	if _, stderr, code := bench.runs(t, "production", "restore", "shop-main-pg", "main", path); code != 0 {
		t.Fatalf("restore exited %d: %s", code, stderr)
	}
	restored, err := os.ReadFile(filepath.Join(bench.bin, "restored"))
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != "dump of shop-main-pg" {
		t.Errorf("pg_restore was fed %q, want the dump", restored)
	}
	if roles, _ := os.ReadFile(filepath.Join(bench.bin, "restored-roles")); string(roles) != "roles of shop-main-pg" {
		t.Errorf("psql was fed %q before the restore, want the roles the dump was taken with", roles)
	}
	if _, _, code := bench.runs(t, "production", "restore", "shop-main-pg", "main", filepath.Join(bench.root, "nothing.dump")); code == 0 {
		t.Error("a restore from a dump that is not there was reported as done")
	}
}

func TestAnApplyOverAHostBootstrappedBeforeBackupsWritesThem(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	var missing []string
	for _, item := range BackupItems() {
		missing = append(missing, item.ID())
	}
	stood := settledOn(t, class)
	stood.stands[class] = slices.DeleteFunc(stood.stands[class], func(item Item) bool { return slices.Contains(missing, item.ID()) })
	report := &said{}
	if err := Bootstrap(stood.host(), testVendor).Apply(context.Background(),
		providerkit.BootstrapRequest{Class: class, Writer: "the-suite"}, report); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	for _, id := range missing {
		if report.at("wrote "+id) < 0 {
			t.Errorf("bootstrap says a host carries %s and an apply never wrote it, so every status after it reads drifted and every re-plan moves it:\n%s",
				id, strings.Join(report.lines, "\n"))
		}
	}
}
