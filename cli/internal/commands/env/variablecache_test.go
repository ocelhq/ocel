package env

import (
	"os"
	"path/filepath"
	"testing"

	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestTheVariableCacheLivesInTheVariableCacheDirectoryOfTheUserConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)

	cache, err := openVariableCache()
	if err != nil {
		t.Fatalf("openVariableCache err = %v", err)
	}
	if want := filepath.Join(home, "ocel", "variable-cache"); cache.dir != want {
		t.Errorf("cache dir = %q, want %q", cache.dir, want)
	}
}

func TestTheVariableCacheReturnsTheGroupsItSaved(t *testing.T) {
	cache, err := openVariableCacheAt(t.TempDir())
	if err != nil {
		t.Fatalf("openVariableCacheAt err = %v", err)
	}
	definitions := []*resourcesv1.VariableDefinition{
		{Key: "GITHUB_CLIENT_ID", Group: "github"},
		{Key: "LOG_LEVEL"},
	}
	groups := []*resourcesv1.GroupDefinition{{Key: "github", Description: "Sign in with GitHub"}}
	if err := cache.Save("/project", "fp", definitions, groups); err != nil {
		t.Fatalf("Save err = %v", err)
	}

	loaded, loadedGroups, ok := cache.LoadContaining("/project", "fp", "GITHUB_CLIENT_ID")
	if !ok {
		t.Fatal("LoadContaining ok = false, want a hit")
	}
	if len(loaded) != 2 {
		t.Errorf("definitions = %v, want both", loaded)
	}
	if len(loadedGroups) != 1 || loadedGroups[0].GetKey() != "github" ||
		loadedGroups[0].GetDescription() != "Sign in with GitHub" {
		t.Errorf("groups = %v, want the declared group passed through", loadedGroups)
	}
}

func TestTheVariableCacheMissesAnotherFingerprint(t *testing.T) {
	cache, err := openVariableCacheAt(t.TempDir())
	if err != nil {
		t.Fatalf("openVariableCacheAt err = %v", err)
	}
	if err := cache.Save("/project", "fp", []*resourcesv1.VariableDefinition{{Key: "LOG_LEVEL"}}, nil); err != nil {
		t.Fatalf("Save err = %v", err)
	}

	if _, _, ok := cache.LoadContaining("/project", "other", "LOG_LEVEL"); ok {
		t.Error("LoadContaining ok = true, want a miss once the project no longer matches what was cached")
	}
}

func TestTheVariableCacheMissesAnEntryWrittenBeforeGroups(t *testing.T) {
	dir := t.TempDir()
	cache, err := openVariableCacheAt(dir)
	if err != nil {
		t.Fatalf("openVariableCacheAt err = %v", err)
	}
	stale := `{"fingerprint":"fp","definitions":[{"key":"GITHUB_CLIENT_ID","group":"github"}]}`
	if err := os.WriteFile(filepath.Join(dir, hashString("/project")+".json"), []byte(stale), 0o600); err != nil {
		t.Fatalf("write stale entry: %v", err)
	}

	if _, _, ok := cache.LoadContaining("/project", "fp", "GITHUB_CLIENT_ID"); ok {
		t.Error("LoadContaining ok = true, want a miss for an entry written before groups")
	}
}
