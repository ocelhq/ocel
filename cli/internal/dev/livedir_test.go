package dev

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/processenv"
)

const kvBinding = `{"name":"cache","kv":{"host":"127.0.0.1","port":6379,"password":"s3cret-dev-password"}}`

func newTestLiveDir(t *testing.T) *liveDir {
	t.Helper()
	live, err := newLiveDir()
	if err != nil {
		t.Fatalf("newLiveDir: %v", err)
	}
	t.Cleanup(live.remove)
	return live
}

func readLiveFile(t *testing.T, env map[string]string, key string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(env[processenv.LiveDirEnvVar], key))
	if err != nil {
		t.Fatalf("read %s from the live dir: %v", key, err)
	}
	return string(raw)
}

func TestTheAppEnvironmentHoldsNoBindingAndNamesTheDirectoryHoldingThem(t *testing.T) {
	live := newTestLiveDir(t)

	env, err := live.project(map[string]string{
		"OCEL_RESOURCE_KV_cache":         kvBinding,
		"OCEL_RESOURCE_TASK_echo-name":   `{"name":"echo-name","task":{}}`,
		processenv.AppFolderEnvVar:       "/web",
		"DATABASE_URL_FROM_THE_DOT_FILE": "postgres://localhost/app",
	})
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	for key, value := range env {
		if strings.HasPrefix(key, processenv.ResourceEnvVarPrefix) {
			t.Errorf("the app environment holds the binding %s", key)
		}
		if strings.Contains(value, "s3cret-dev-password") {
			t.Errorf("%s holds the store's password in clear", key)
		}
	}
	if env[processenv.AppFolderEnvVar] != "/web" || env["DATABASE_URL_FROM_THE_DOT_FILE"] != "postgres://localhost/app" {
		t.Errorf("the app environment lost a value that is not a binding: %v", env)
	}
	if got := readLiveFile(t, env, "OCEL_RESOURCE_KV_cache"); got != kvBinding {
		t.Errorf("OCEL_RESOURCE_KV_cache in the live dir = %q, want %q", got, kvBinding)
	}
	if got := readLiveFile(t, env, "OCEL_RESOURCE_TASK_echo-name"); got != `{"name":"echo-name","task":{}}` {
		t.Errorf("OCEL_RESOURCE_TASK_echo-name in the live dir = %q", got)
	}
}

func TestOnlyTheUserReadsTheBindingFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits")
	}
	live := newTestLiveDir(t)
	env, err := live.project(map[string]string{"OCEL_RESOURCE_KV_cache": kvBinding})
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	dir, err := os.Stat(env[processenv.LiveDirEnvVar])
	if err != nil {
		t.Fatalf("stat the live dir: %v", err)
	}
	if mode := dir.Mode().Perm(); mode != 0o700 {
		t.Errorf("the live dir mode = %o, want 700", mode)
	}
	file, err := os.Stat(filepath.Join(env[processenv.LiveDirEnvVar], "OCEL_RESOURCE_KV_cache"))
	if err != nil {
		t.Fatalf("stat the binding file: %v", err)
	}
	if mode := file.Mode().Perm(); mode != 0o600 {
		t.Errorf("the binding file mode = %o, want 600", mode)
	}
}

func TestAReprojectionReplacesChangedBindingsAndDropsRemovedOnes(t *testing.T) {
	live := newTestLiveDir(t)
	if _, err := live.project(map[string]string{
		"OCEL_RESOURCE_KV_cache":        kvBinding,
		"OCEL_RESOURCE_POSTGRES_legacy": `{"name":"legacy"}`,
	}); err != nil {
		t.Fatalf("project: %v", err)
	}

	changed := `{"name":"cache","kv":{"password":"rotated"}}`
	env, err := live.project(map[string]string{"OCEL_RESOURCE_KV_cache": changed})
	if err != nil {
		t.Fatalf("project again: %v", err)
	}

	if got := readLiveFile(t, env, "OCEL_RESOURCE_KV_cache"); got != changed {
		t.Errorf("OCEL_RESOURCE_KV_cache = %q after the change, want %q", got, changed)
	}
	entries, err := os.ReadDir(env[processenv.LiveDirEnvVar])
	if err != nil {
		t.Fatalf("read the live dir: %v", err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if strings.Join(names, ",") != "OCEL_RESOURCE_KV_cache" {
		t.Errorf("the live dir holds %v, want only the binding still declared", names)
	}
}

func TestABindingKeyThatCannotNameAFileIsRefused(t *testing.T) {
	live := newTestLiveDir(t)
	for _, key := range []string{"OCEL_RESOURCE_KV_a/b", `OCEL_RESOURCE_KV_a\b`} {
		if _, err := live.project(map[string]string{key: kvBinding}); err == nil {
			t.Errorf("project(%q) = nil error, want a refusal", key)
		}
	}
}

func TestRemovingTheLiveDirLeavesNoBindingOnDisk(t *testing.T) {
	live, err := newLiveDir()
	if err != nil {
		t.Fatalf("newLiveDir: %v", err)
	}
	env, err := live.project(map[string]string{"OCEL_RESOURCE_KV_cache": kvBinding})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	live.remove()
	if _, err := os.Stat(env[processenv.LiveDirEnvVar]); !os.IsNotExist(err) {
		t.Errorf("the live dir survives its removal: %v", err)
	}
}

func TestAReprojectionLeavesTheBindingsAnEarlierProjectionHandedOutWholeUntilTheyAreRetired(t *testing.T) {
	live := newTestLiveDir(t)
	earlier, err := live.project(map[string]string{"OCEL_RESOURCE_KV_cache": kvBinding})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	rotated := `{"name":"cache","kv":{"password":"rotated"}}`
	later, err := live.project(map[string]string{
		"OCEL_RESOURCE_KV_cache":      rotated,
		"OCEL_RESOURCE_POSTGRES_main": `{"name":"main"}`,
	})
	if err != nil {
		t.Fatalf("project again: %v", err)
	}

	if earlier[processenv.LiveDirEnvVar] == later[processenv.LiveDirEnvVar] {
		t.Fatalf("both projections name %s, so a reader of the earlier one sees the later one's files", earlier[processenv.LiveDirEnvVar])
	}
	if got := readLiveFile(t, earlier, "OCEL_RESOURCE_KV_cache"); got != kvBinding {
		t.Errorf("the earlier projection's OCEL_RESOURCE_KV_cache = %q while it is still read, want %q", got, kvBinding)
	}
	if _, err := os.Stat(filepath.Join(earlier[processenv.LiveDirEnvVar], "OCEL_RESOURCE_POSTGRES_main")); !os.IsNotExist(err) {
		t.Errorf("the earlier projection gained a binding the later one added: %v", err)
	}
	if got := readLiveFile(t, later, "OCEL_RESOURCE_KV_cache"); got != rotated {
		t.Errorf("the later projection's OCEL_RESOURCE_KV_cache = %q, want %q", got, rotated)
	}

	live.retire()
	if _, err := os.Stat(earlier[processenv.LiveDirEnvVar]); !os.IsNotExist(err) {
		t.Errorf("the earlier projection survives once its readers are retired: %v", err)
	}
	if got := readLiveFile(t, later, "OCEL_RESOURCE_KV_cache"); got != rotated {
		t.Errorf("retiring removed the projection still in use: OCEL_RESOURCE_KV_cache = %q", got)
	}
}

func TestAnUnchangedReprojectionHandsOutTheSameDirectory(t *testing.T) {
	live := newTestLiveDir(t)
	bindings := map[string]string{"OCEL_RESOURCE_KV_cache": kvBinding}
	first, err := live.project(bindings)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	again, err := live.project(bindings)
	if err != nil {
		t.Fatalf("project again: %v", err)
	}
	if first[processenv.LiveDirEnvVar] != again[processenv.LiveDirEnvVar] {
		t.Errorf("an unchanged set of bindings moved from %s to %s", first[processenv.LiveDirEnvVar], again[processenv.LiveDirEnvVar])
	}
}
