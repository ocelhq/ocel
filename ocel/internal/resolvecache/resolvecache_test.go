package resolvecache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCache_SaveThenLoad_RoundTripsAndUses0600(t *testing.T) {
	dir := t.TempDir()
	cache, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}

	want := Entry{
		DefsHash:  "hash_1",
		Account:   "acct_1",
		ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second).UTC(),
		Env:       map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"connectionString":"postgres://x"}`},
	}
	if err := cache.Save("proj_1", want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, ok := cache.Load("proj_1")
	if !ok {
		t.Fatal("Load: expected an entry, got none")
	}
	if got.DefsHash != want.DefsHash || got.Account != want.Account || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("got = %+v, want %+v", got, want)
	}
	if got.Env["OCEL_RESOURCE_POSTGRES_main"] != want.Env["OCEL_RESOURCE_POSTGRES_main"] {
		t.Fatalf("Env = %+v, want %+v", got.Env, want.Env)
	}

	info, err := os.Stat(filepath.Join(dir, "proj_1.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 0600", perm)
	}
}

func TestCache_Load_MissingEntryIsNotFoundNotError(t *testing.T) {
	cache, err := OpenAt(t.TempDir())
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	if _, ok := cache.Load("does_not_exist"); ok {
		t.Fatal("Load: expected no entry for an unsaved project")
	}
}

func TestCache_Load_CorruptFileIsTreatedAsAMiss(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "proj_1.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	cache, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	if _, ok := cache.Load("proj_1"); ok {
		t.Fatal("Load: expected a corrupt cache file to be treated as a miss")
	}
}

func TestCache_ScopesEntriesPerProject(t *testing.T) {
	cache, err := OpenAt(t.TempDir())
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	if err := cache.Save("proj_a", Entry{DefsHash: "a"}); err != nil {
		t.Fatalf("Save proj_a: %v", err)
	}
	if err := cache.Save("proj_b", Entry{DefsHash: "b"}); err != nil {
		t.Fatalf("Save proj_b: %v", err)
	}

	a, ok := cache.Load("proj_a")
	if !ok || a.DefsHash != "a" {
		t.Fatalf("proj_a entry = %+v, ok=%v", a, ok)
	}
	b, ok := cache.Load("proj_b")
	if !ok || b.DefsHash != "b" {
		t.Fatalf("proj_b entry = %+v, ok=%v", b, ok)
	}
}

func TestHashDefs_IsOrderIndependentButTypeAndNameSensitive(t *testing.T) {
	a := HashDefs([]Def{{Name: "main", Type: "POSTGRES"}, {Name: "second", Type: "POSTGRES"}})
	b := HashDefs([]Def{{Name: "second", Type: "POSTGRES"}, {Name: "main", Type: "POSTGRES"}})
	if a != b {
		t.Fatalf("HashDefs should be order-independent: %q != %q", a, b)
	}

	changed := HashDefs([]Def{{Name: "main", Type: "MYSQL"}, {Name: "second", Type: "POSTGRES"}})
	if a == changed {
		t.Fatal("HashDefs should change when a definition's type changes")
	}
}

func TestFingerprint_ChangesWithBaseURLOrToken(t *testing.T) {
	base := Fingerprint("https://api.example.com", "tok_a")
	if Fingerprint("https://api.example.com", "tok_b") == base {
		t.Fatal("Fingerprint should change when the token changes")
	}
	if Fingerprint("https://other.example.com", "tok_a") == base {
		t.Fatal("Fingerprint should change when the base URL changes")
	}
}
