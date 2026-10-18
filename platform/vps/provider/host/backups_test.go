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

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

type backupBench struct {
	root string
	bin  string
}

const benchStoreSecret = "s3cr3t-of-the-store"

func backupsOn(t *testing.T, labelled ...string) backupBench {
	t.Helper()
	bench := backupBench{root: t.TempDir(), bin: t.TempDir()}
	labels := filepath.Join(bench.bin, "labels")
	docker := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >>" + filepath.Join(bench.bin, "log") + "\n" +
		"for name; do :; done\n" +
		"tab=$(printf '\\t')\n" +
		"case \"$1\" in\n" +
		"ps) cut -f1,2 " + filepath.Join(bench.bin, "labelled") + " ;;\n" +
		"inspect)\n" +
		"  line=$(grep -m1 \"^$name$tab\" " + labels + ") || { echo \"Error: No such container: $name\" >&2; exit 1; }\n" +
		"  case \"$*\" in\n" +
		"  *ocel.backup*) printf '%s\\n' \"$line\" | cut -f4 ;;\n" +
		"  *ocel.resource*) printf '%s\\n' \"$line\" | cut -f3 ;;\n" +
		"  *ocel.database-owner*) printf '%s\\n' \"$line\" | cut -s -f5 ;;\n" +
		"  *.Mounts*) cat " + filepath.Join(bench.bin, "mounted") + "; echo ;;\n" +
		"  *.Config.Env*) printf '%s\\n' RUSTFS_ACCESS_KEY=ocel RUSTFS_SECRET_KEY=" + benchStoreSecret + " RUSTFS_ADDRESS=:9000 ;;\n" +
		"  *Networks*) echo ocel-shop-production ;;\n" +
		"  esac ;;\n" +
		"run)\n" +
		"  prev=''\n" +
		"  for arg; do\n" +
		"    case \"$prev\" in --env-file) cp \"$arg\" " + filepath.Join(bench.bin, "copier-env") + " ;; esac\n" +
		"    case \"$arg\" in type=bind,src=*,dst=/backup) src=${arg#type=bind,src=}; src=${src%,dst=/backup}; mkdir -p \"$src/current/uploads\"; printf object >\"$src/current/uploads/object\" ;; esac\n" +
		"    prev=$arg\n" +
		"  done\n" +
		"  [ ! -f " + filepath.Join(bench.bin, "copier-fails") + " ] || exit 1 ;;\n" +
		"start) [ ! -f " + filepath.Join(bench.bin, "start-fails") + " ] || { rm -f " + filepath.Join(bench.bin, "start-fails") + "; exit 1; } ;;\n" +
		"exec)\n" +
		"  for arg in \"$@\"; do case $arg in\n" +
		"    pg_dump) if [ -f " + filepath.Join(bench.bin, "broken") + " ] || [ -f " + filepath.Join(bench.bin, "broken-") + "\"$2\" ]; then printf 'half a du'; echo 'pg_dump: connection refused' >&2; exit 1; fi\n" +
		"             printf 'dump of %s' \"$2\"; exit 0 ;;\n" +
		"    pg_dumpall) printf 'roles of %s' \"$2\"; exit 0 ;;\n" +
		"    pg_restore) cat >" + filepath.Join(bench.bin, "restored") + "; exit 0 ;;\n" +
		"    psql) cat >" + filepath.Join(bench.bin, "restored-roles") + "; exit 0 ;;\n" +
		"  esac; done ;;\n" +
		"esac\n"
	df := "#!/bin/sh\nprintf 'Avail\\n%s\\n' \"$(cat " + filepath.Join(bench.bin, "free") + ")\"\n"
	for name, body := range map[string]string{"docker": docker, "df": df} {
		executable(t, filepath.Join(bench.bin, name), body)
	}
	bench.stubs(t, "labelled", strings.Join(labelled, "\n")+"\n")
	bench.stubs(t, "labels", strings.Join(append([]string{"shop-main-pg\tproduction\tmain\tpg", "shop-cache-kv\tproduction\tcache\tkv", "shop-store-s3\tproduction\tstore\ts3"}, labelled...), "\n")+"\n")
	bench.stubs(t, "free", "1000000000")
	mounted := filepath.Join(bench.root, "volume")
	if err := os.MkdirAll(filepath.Join(mounted, "appendonlydir"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"dump.rdb": "snapshot valkey saved", "appendonlydir/appendonly.aof.3.incr.aof": "writes since"} {
		if err := os.WriteFile(filepath.Join(mounted, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bench.stubs(t, "mounted", mounted)
	return bench
}

func (b backupBench) stubs(t *testing.T, name, body string) {
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

func (b backupBench) kept(t *testing.T, tier, container string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(b.root, tier, "backups", container))
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
	said, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg")
	if code != 0 {
		t.Fatalf("dump exited %d: %s", code, stderr)
	}
	kept := bench.kept(t, "production", "shop-main-pg")
	if len(kept) != 2 || !strings.HasSuffix(kept[0], ".dump") || !strings.HasSuffix(kept[1], ".roles.sql") {
		t.Fatalf("the backups contain %v, want the one dump, the roles it was taken with, and nothing half-written beside them", kept)
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
		t.Errorf("the dump contains %q, want what pg_dump wrote", body)
	}
}

func TestADumpThatFailsLeavesNothingBehindAndKeepsWhatWasThere(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	if _, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg"); code != 0 {
		t.Fatalf("the first dump exited %d: %s", code, stderr)
	}
	before := bench.kept(t, "production", "shop-main-pg")
	bench.stubs(t, "broken", "")
	_, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg")
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
	if _, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg"); code != 0 {
		t.Fatalf("dump exited %d: %s", code, stderr)
	}
	kept := slices.DeleteFunc(bench.kept(t, "production", "shop-main-pg"), func(name string) bool { return !strings.HasSuffix(name, ".dump") })
	if len(kept) != 7 {
		t.Fatalf("the backups contain %d dumps, want the newest 7: %v", len(kept), kept)
	}
	if all := bench.kept(t, "production", "shop-main-pg"); len(all) != 8 {
		t.Errorf("the backups contain %v, want 7 dumps and the roles of the one just taken: roles whose dump is gone are bytes nothing reads", all)
	}
	if slices.Contains(kept, "20260901T000000Z.dump") || !slices.Contains(kept, "20260909T000000Z.dump") {
		t.Errorf("the backups kept %v, and the ones to go are the oldest", kept)
	}
}

func TestADumpIsRefusedWhenTheDiskHasNoRoomForAnotherOne(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	dir := filepath.Join(bench.root, "production", "backups", "shop-main-pg")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20260901T000000Z.dump"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	bench.stubs(t, "free", "4096")
	_, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg")
	if code == 0 {
		t.Fatal("a dump was taken onto a disk with room for less than two of the last one, and a full disk takes the database down with it")
	}
	if !strings.Contains(stderr, "free") {
		t.Errorf("the refusal says %q and never what is short", stderr)
	}
}

func TestASweepBacksUpEveryResourceThatAsksEachTheWayItsStoreCopiesOnline(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t,
		"shop-main-pg\tproduction\tmain\tpg",
		"blog-posts-pg\tpreview\tposts\tpg",
		"shop-cache-kv\tproduction\tcache\tkv",
		"shop-store-s3\tproduction\tstore\ts3",
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
	if got := bench.kept(t, "production", "shop-cache-kv"); len(got) != 1 || !strings.HasSuffix(got[0], ".rdb") {
		t.Errorf("shop's kv has %v, want the one snapshot Valkey saved", got)
	}
	if got := bench.kept(t, "production", "shop-store-s3"); !slices.Contains(got, "current") {
		t.Errorf("shop's store has %v, want the copy of its objects", got)
	}
	for _, line := range bench.drove(t) {
		if strings.HasPrefix(line, "pause ") || strings.HasPrefix(line, "stop ") {
			t.Errorf("a backup ran `docker %s`, and the resource stopped answering for as long as its copy took", line)
		}
	}
}

func TestASweepWhoseFirstTwoBackupsFailStillBacksUpEveryOtherResource(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t,
		"shop-a-pg\tproduction\ta\tpg",
		"shop-b-pg\tproduction\tb\tpg",
		"shop-c-pg\tproduction\tc\tpg",
		"shop-d-pg\tproduction\td\tpg",
	)
	bench.stubs(t, "broken-shop-a-pg", "")
	bench.stubs(t, "broken-shop-b-pg", "")
	_, stderr, code := bench.runs(t, "sweep")
	if code == 0 {
		t.Errorf("a sweep in which two backups failed exited 0, so the timer reports a night with no backup of them as a success: %s", stderr)
	}
	for _, container := range []string{"shop-c-pg", "shop-d-pg"} {
		if got := bench.kept(t, "production", container); len(got) != 2 {
			t.Errorf("%s has %v after a sweep in which two other backups failed, so one night's failures cost every backup after them", container, got)
		}
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

func TestAStoreIsCopiedObjectByObjectOverItsOwnNetworkWithItsCredentialOffTheCommandLine(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	said, stderr, code := bench.runs(t, "production", "dump", "shop-store-s3")
	if code != 0 {
		t.Fatalf("dump exited %d: %s", code, stderr)
	}
	dir := filepath.Join(bench.root, "production", "backups", "shop-store-s3")
	if said != filepath.Join(dir, "current") {
		t.Errorf("dump said %q, want the copy it keeps current", said)
	}
	var copied string
	for _, line := range bench.drove(t) {
		if strings.HasPrefix(line, "run ") {
			copied = line
		}
	}
	for _, want := range []string{"--network ocel-shop-production", StoreCopierImage, "sync store: /backup/current --backup-dir /backup/2", "--cap-drop ALL"} {
		if !strings.Contains(copied, want) {
			t.Errorf("the copy ran `docker %s`, want it to hold %q", copied, want)
		}
	}
	if strings.Contains(copied, benchStoreSecret) {
		t.Errorf("the store's secret is on the copy's command line, where every process on the box reads it: %s", copied)
	}
	env, err := os.ReadFile(filepath.Join(bench.bin, "copier-env"))
	if err != nil || !strings.Contains(string(env), "RCLONE_CONFIG_STORE_SECRET_ACCESS_KEY="+benchStoreSecret) {
		t.Errorf("the copy was handed %q, and without the store's secret it signs nothing it reads", env)
	}
	if body, _ := os.ReadFile(filepath.Join(said, "uploads", "object")); string(body) != "object" {
		t.Errorf("the copy holds %q", body)
	}
}

func TestAStoreCopyThatFailsIsNotReportedAsABackup(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	bench.stubs(t, "copier-fails", "")
	if _, _, code := bench.runs(t, "production", "dump", "shop-store-s3"); code == 0 {
		t.Fatal("a copy that failed was reported as a backup")
	}
}

func TestAKVBackupIsTheSnapshotValkeySavedTakenWithoutStoppingIt(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	said, stderr, code := bench.runs(t, "production", "dump", "shop-cache-kv")
	if code != 0 {
		t.Fatalf("dump exited %d: %s", code, stderr)
	}
	if body, _ := os.ReadFile(said); string(body) != "snapshot valkey saved" {
		t.Errorf("the backup holds %q, want the snapshot Valkey saved", body)
	}
	if err := os.Remove(filepath.Join(bench.root, "volume", "dump.rdb")); err != nil {
		t.Fatal(err)
	}
	if _, _, code := bench.runs(t, "production", "dump", "shop-cache-kv"); code == 0 {
		t.Error("a store that has saved no snapshot was reported as backed up")
	}
}

func TestAKVRestoreLoadsTheSnapshotAsTheBaseOfTheAppendOnlyFileValkeyReadsFirst(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	snapshot := filepath.Join(bench.root, "kept.rdb")
	if err := os.WriteFile(snapshot, []byte("restored snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	said, stderr, code := bench.runs(t, "production", "restore", "shop-cache-kv", snapshot)
	if code != 0 {
		t.Fatalf("restore exited %d: %s", code, stderr)
	}
	volume := filepath.Join(bench.root, "volume")
	if base, _ := os.ReadFile(filepath.Join(volume, "appendonlydir", "appendonly.aof.1.base.rdb")); string(base) != "restored snapshot" {
		t.Errorf("the append-only base holds %q, and Valkey with appendonly on loads that, never dump.rdb", base)
	}
	if manifest, _ := os.ReadFile(filepath.Join(volume, "appendonlydir", "appendonly.aof.manifest")); string(manifest) != "file appendonly.aof.1.base.rdb seq 1 type b\n" {
		t.Errorf("the manifest reads %q", manifest)
	}
	if _, err := os.Stat(filepath.Join(volume, "appendonlydir", "appendonly.aof.3.incr.aof")); err == nil {
		t.Error("the writes since the old base stayed beside the restored one, and Valkey would replay them over it")
	}
	aside := strings.TrimPrefix(said, "what shop-cache-kv held before the restore is at ")
	if kept, _ := os.ReadFile(filepath.Join(aside, "appendonlydir", "appendonly.aof.3.incr.aof")); string(kept) != "writes since" {
		t.Errorf("what the store held before the restore is gone: %q says it is at %s", said, aside)
	}
	drove := bench.drove(t)
	stopped, started := slices.IndexFunc(drove, func(line string) bool { return strings.HasPrefix(line, "stop ") }), slices.Index(drove, "start shop-cache-kv")
	if stopped < 0 || started < stopped {
		t.Errorf("the restore swapped Valkey's files without stopping it first and starting it after:\n%s", strings.Join(drove, "\n"))
	}
}

func TestAKVRestoreThatCannotStartTheStorePutsBackWhatItHeld(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	snapshot := filepath.Join(bench.root, "kept.rdb")
	if err := os.WriteFile(snapshot, []byte("restored snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	bench.stubs(t, "start-fails", "")
	if _, _, code := bench.runs(t, "production", "restore", "shop-cache-kv", snapshot); code == 0 {
		t.Fatal("a restore whose store never started was reported as done")
	}
	volume := filepath.Join(bench.root, "volume")
	if kept, _ := os.ReadFile(filepath.Join(volume, "appendonlydir", "appendonly.aof.3.incr.aof")); string(kept) != "writes since" {
		t.Error("a restore that failed left the store on neither its own data nor the snapshot")
	}
	if body, _ := os.ReadFile(filepath.Join(volume, "dump.rdb")); string(body) != "snapshot valkey saved" {
		t.Errorf("a restore that failed left dump.rdb holding %q", body)
	}
	if starts := slices.DeleteFunc(bench.drove(t), func(line string) bool { return line != "start shop-cache-kv" }); len(starts) != 2 {
		t.Errorf("the store was started %d times, want the failed start and the one on the data it had", len(starts))
	}
}

func TestAStoreRestoreCopiesTheKeptObjectsBackFromAReadOnlyMount(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	if _, stderr, code := bench.runs(t, "production", "dump", "shop-store-s3"); code != 0 {
		t.Fatalf("dump exited %d: %s", code, stderr)
	}
	if _, stderr, code := bench.runs(t, "production", "restore", "shop-store-s3"); code != 0 {
		t.Fatalf("restore exited %d: %s", code, stderr)
	}
	drove := bench.drove(t)
	restored := drove[len(drove)-1]
	for _, want := range []string{"dst=/backup,readonly", "sync /backup store:"} {
		if !strings.Contains(restored, want) {
			t.Errorf("the restore ran `docker %s`, want it to hold %q", restored, want)
		}
	}
}

func TestARestoreFeedsTheDumpBackThroughTheContainer(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t)
	path, stderr, code := bench.runs(t, "production", "dump", "shop-main-pg")
	if code != 0 {
		t.Fatalf("dump exited %d: %s", code, stderr)
	}
	if _, stderr, code := bench.runs(t, "production", "restore", "shop-main-pg", path); code != 0 {
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
	if _, _, code := bench.runs(t, "production", "restore", "shop-main-pg", filepath.Join(bench.root, "nothing.dump")); code == 0 {
		t.Error("a restore from a dump that is not there was reported as done")
	}
}

func TestADatabaseARoleOwnsIsRestoredAsThatRoleSoItStillOwnsWhatItRestores(t *testing.T) {
	t.Parallel()

	bench := backupsOn(t, "shop-queue-pg\tproduction\tocel\tpg\tocel_queues")
	path, stderr, code := bench.runs(t, "production", "dump", "shop-queue-pg")
	if code != 0 {
		t.Fatalf("dump exited %d: %s", code, stderr)
	}
	if _, stderr, code := bench.runs(t, "production", "restore", "shop-queue-pg", path); code != 0 {
		t.Fatalf("restore exited %d: %s", code, stderr)
	}
	restored := slices.IndexFunc(bench.drove(t), func(line string) bool { return strings.Contains(line, "pg_restore") })
	if restored < 0 || !strings.Contains(bench.drove(t)[restored], "--role=ocel_queues") {
		t.Errorf("the restore ran %v, and tables pg_restore makes as the superuser are ones the role that owns the database cannot use", bench.drove(t))
	}
	if _, _, code := bench.runs(t, "production", "restore", "shop-main-pg", path); code != 0 {
		t.Fatal("a database no role owns could not be restored")
	}
	last := bench.drove(t)
	if strings.Contains(last[len(last)-1], "--role") {
		t.Errorf("a database no role owns was restored as one: %s", last[len(last)-1])
	}
}

func TestAnApplyOverAHostBootstrappedBeforeBackupsWritesThem(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	var missing []string
	for _, item := range BackupItems() {
		missing = append(missing, item.ID())
	}
	older := bootstrappedOn(t, tier)
	older.installed[tier] = slices.DeleteFunc(older.installed[tier], func(item Item) bool { return slices.Contains(missing, item.ID()) })
	progress := &said{}
	if err := NewBootstrap(older.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "the-suite"}, progress); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	for _, item := range BackupItems() {
		if progress.at("Installed "+item.phrase()) < 0 {
			t.Errorf("bootstrap says a host has %s and an apply never wrote it, so every status after it reads drifted and every re-plan moves it:\n%s",
				item.ID(), strings.Join(progress.lines, "\n"))
		}
	}
}
