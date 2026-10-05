package userconfig_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"

	"github.com/ocelhq/ocel/cli/internal/userconfig"
)

func isolatedHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	return home
}

func settingsPath(t *testing.T) string {
	t.Helper()
	dir, err := userconfig.Dir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "settings.json")
}

func writeSettingsFile(t *testing.T, content string) string {
	t.Helper()
	if _, err := userconfig.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	path := settingsPath(t)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func holdSettingsLock(t *testing.T) func() {
	t.Helper()
	dir, err := userconfig.EnsureDir()
	if err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(dir, "settings.lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	return func() { _ = lock.Unlock() }
}

func TestReadWaitsForAWriterHoldingTheSettingsLock(t *testing.T) {
	isolatedHome(t)
	writeSettingsFile(t, `{"k":"v"}`)
	release := holdSettingsLock(t)

	read := make(chan userconfig.Settings)
	go func() { read <- userconfig.Read() }()
	select {
	case got := <-read:
		release()
		t.Fatalf("Read() returned %v while a writer held the lock", got)
	case <-time.After(200 * time.Millisecond):
	}
	release()
	if got := <-read; string(got["k"]) != `"v"` {
		t.Errorf("Read() = %v, want k=\"v\"", got)
	}
}

func TestReadWithoutALockFileReadsTheSettingsFile(t *testing.T) {
	isolatedHome(t)
	writeSettingsFile(t, `{"k":"v"}`)
	if got := userconfig.Read(); string(got["k"]) != `"v"` {
		t.Errorf("Read() = %v, want k=\"v\"", got)
	}
	dir, err := userconfig.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.lock")); !os.IsNotExist(err) {
		t.Errorf("Read() created the lock file: stat err = %v", err)
	}
}

func TestReadWithoutAConfigDirectoryIsEmptyAndCreatesNothing(t *testing.T) {
	isolatedHome(t)
	if got := userconfig.Read(); len(got) != 0 {
		t.Errorf("Read() = %v, want empty", got)
	}
	dir, err := userconfig.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Read() created %s: stat err = %v", dir, err)
	}
}

func TestConcurrentReadsAndUpdatesAlwaysSeeAWholeFile(t *testing.T) {
	isolatedHome(t)
	if err := userconfig.Update(func(s userconfig.Settings) { s["k"] = json.RawMessage(`"v"`) }); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := userconfig.Update(func(s userconfig.Settings) { s["k"] = json.RawMessage(`"v"`) }); err != nil {
					t.Error(err)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if got := userconfig.Read(); string(got["k"]) != `"v"` {
					t.Errorf("Read() = %v mid-update", got)
				}
			}
		}()
	}
	wg.Wait()
}

func TestDirIsOcelUnderTheUserConfigDirectory(t *testing.T) {
	home := isolatedHome(t)
	base, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if base != home {
		t.Fatalf("test setup: user config dir = %q, want %q", base, home)
	}

	dir, err := userconfig.Dir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "ocel"); dir != want {
		t.Errorf("Dir() = %q, want %q", dir, want)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Dir() created the directory: stat err = %v", err)
	}
}

func TestEnsureDirCreatesTheDirectoryReadableOnlyByTheUser(t *testing.T) {
	isolatedHome(t)
	dir, err := userconfig.EnsureDir()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %o, want 700", info.Mode().Perm())
	}
	again, err := userconfig.EnsureDir()
	if err != nil || again != dir {
		t.Errorf("second EnsureDir() = %q, %v; want %q", again, err, dir)
	}
}

func TestEnsureInstallIDIsAUUIDAndStable(t *testing.T) {
	isolatedHome(t)
	first, err := userconfig.EnsureInstallID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("EnsureInstallID() = %q, not a UUID: %v", first, err)
	}
	second, err := userconfig.EnsureInstallID()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("EnsureInstallID() changed: %q then %q", first, second)
	}
}

func TestEnsureInstallIDDiffersBetweenConfigHomes(t *testing.T) {
	isolatedHome(t)
	a, err := userconfig.EnsureInstallID()
	if err != nil {
		t.Fatal(err)
	}
	isolatedHome(t)
	b, err := userconfig.EnsureInstallID()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two config homes share install ID %q", a)
	}
}

func TestEnsureInstallIDIsReadFromTheSettingsFile(t *testing.T) {
	isolatedHome(t)
	want := uuid.NewString()
	writeSettingsFile(t, `{"install_id":"`+want+`"}`)
	got, err := userconfig.EnsureInstallID()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("EnsureInstallID() = %q, want %q", got, want)
	}
}

func TestEnsureInstallIDIsReplacedWhenTheStoredOneIsNotAUUID(t *testing.T) {
	isolatedHome(t)
	writeSettingsFile(t, `{"install_id":42}`)
	got, err := userconfig.EnsureInstallID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("EnsureInstallID() = %q, not a UUID", got)
	}
	again, err := userconfig.EnsureInstallID()
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Errorf("replacement is not stable: %q then %q", got, again)
	}
}

func TestSettingsFileIsReadableOnlyByTheUser(t *testing.T) {
	isolatedHome(t)
	if _, err := userconfig.EnsureInstallID(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestCorruptFileReadsAsEmptyAndIsReplacedOnNextWrite(t *testing.T) {
	isolatedHome(t)
	for _, corrupt := range []string{"{not json", "", "[1,2]", "null", "\x00\x01"} {
		path := writeSettingsFile(t, corrupt)
		if got := userconfig.Read(); len(got) != 0 {
			t.Errorf("Read() of %q = %v, want empty", corrupt, got)
		}
		if err := userconfig.Update(func(s userconfig.Settings) { s["k"] = json.RawMessage(`"v"`) }); err != nil {
			t.Fatalf("Update over %q: %v", corrupt, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]string
		if err := json.Unmarshal(raw, &parsed); err != nil || parsed["k"] != "v" || len(parsed) != 1 {
			t.Errorf("after Update over %q file = %q, err %v", corrupt, raw, err)
		}
	}
}

func TestUnreadableSettingsPathReadsAsEmpty(t *testing.T) {
	isolatedHome(t)
	if _, err := userconfig.EnsureDir(); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(settingsPath(t), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := userconfig.Read(); len(got) != 0 {
		t.Errorf("Read() = %v, want empty", got)
	}
}

func TestUpdatePreservesFieldsItDoesNotKnow(t *testing.T) {
	isolatedHome(t)
	path := writeSettingsFile(t, `{"other":{"nested":[1,2]},"banner_shown":true}`)
	if _, err := userconfig.EnsureInstallID(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	var other bytes.Buffer
	if err := json.Compact(&other, parsed["other"]); err != nil {
		t.Fatal(err)
	}
	if string(parsed["banner_shown"]) != "true" || other.String() != `{"nested":[1,2]}` {
		t.Errorf("unknown fields lost: %s", raw)
	}
	if _, ok := parsed["install_id"]; !ok {
		t.Errorf("install_id missing: %s", raw)
	}
}

func TestUpdateLeavesNoStagingFilesBehind(t *testing.T) {
	isolatedHome(t)
	for i := 0; i < 5; i++ {
		if err := userconfig.Update(func(s userconfig.Settings) { s["n"] = json.RawMessage(`1`) }); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := userconfig.Dir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "settings.json" && e.Name() != "settings.lock" {
			t.Errorf("stray file %q", e.Name())
		}
	}
}

func TestConcurrentUpdatesLeaveAValidFile(t *testing.T) {
	isolatedHome(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if err := userconfig.Update(func(s userconfig.Settings) { s["writer"] = json.RawMessage(`"x"`) }); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	raw, err := os.ReadFile(settingsPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]string
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed["writer"] != "x" {
		t.Fatalf("file = %q, err %v", raw, err)
	}
}

func TestConcurrentFirstInstallIDCreationConverges(t *testing.T) {
	isolatedHome(t)
	ids := make([]string, 32)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := userconfig.EnsureInstallID()
			if err != nil {
				t.Error(err)
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("callers saw different install IDs: %q and %q", ids[0], id)
		}
	}
	stored, err := userconfig.EnsureInstallID()
	if err != nil {
		t.Fatal(err)
	}
	if stored != ids[0] {
		t.Errorf("stored ID %q differs from returned %q", stored, ids[0])
	}
}

func TestConcurrentFirstInstallIDCreationAcrossProcessesConverges(t *testing.T) {
	isolatedHome(t)
	t.Setenv(printInstallIDEnvVar, "1")
	ids := make([]string, 8)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := exec.Command(os.Args[0]).Output()
			if err != nil {
				t.Error(err)
			}
			ids[i] = string(out)
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil || id != ids[0] {
			t.Fatalf("processes saw install IDs %q", ids)
		}
	}
	if stored, err := userconfig.EnsureInstallID(); err != nil || stored != ids[0] {
		t.Errorf("stored ID %q, %v; want %q", stored, err, ids[0])
	}
}
