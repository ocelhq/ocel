package resolvecache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openAt(t *testing.T, dir string) *Cache {
	t.Helper()
	cache, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	return cache
}

func TestCache(t *testing.T) {
	t.Parallel()

	t.Run("save then load round trips and uses 0600", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cache := openAt(t, dir)

		want := Entry{
			DefsHash:  "hash_1",
			Account:   "acct_1",
			ExpiresAt: time.Now().Add(time.Hour).Truncate(time.Second).UTC(),
			Env:       map[string]string{"OCEL_RESOURCE_POSTGRES_main": `{"name":"main","postgres":{"host":"resolved","port":5432,"database":"main","username":"u","password":"p"}}`},
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
	})

	t.Run("a missing entry is not found, not an error", func(t *testing.T) {
		t.Parallel()
		cache := openAt(t, t.TempDir())
		if _, ok := cache.Load("does_not_exist"); ok {
			t.Fatal("Load: expected no entry for an unsaved project")
		}
	})

	t.Run("a corrupt file is treated as a miss", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "proj_1.json"), []byte("not json"), 0o600); err != nil {
			t.Fatalf("seed corrupt file: %v", err)
		}
		cache := openAt(t, dir)
		if _, ok := cache.Load("proj_1"); ok {
			t.Fatal("Load: expected a corrupt cache file to be treated as a miss")
		}
	})

	t.Run("entries are scoped per project", func(t *testing.T) {
		t.Parallel()
		cache := openAt(t, t.TempDir())
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
	})
}

func TestHashDefs(t *testing.T) {
	t.Parallel()

	base := HashDefs([]Def{{Name: "main", Type: "POSTGRES"}, {Name: "second", Type: "POSTGRES"}})

	t.Run("is order independent", func(t *testing.T) {
		t.Parallel()
		reordered := HashDefs([]Def{{Name: "second", Type: "POSTGRES"}, {Name: "main", Type: "POSTGRES"}})
		if base != reordered {
			t.Fatalf("HashDefs should be order-independent: %q != %q", base, reordered)
		}
	})

	t.Run("is type and name sensitive", func(t *testing.T) {
		t.Parallel()
		changed := HashDefs([]Def{{Name: "main", Type: "MYSQL"}, {Name: "second", Type: "POSTGRES"}})
		if base == changed {
			t.Fatal("HashDefs should change when a definition's type changes")
		}
	})
}

func TestFingerprint(t *testing.T) {
	t.Parallel()

	base := Fingerprint("https://api.example.com", "tok_a")

	t.Run("changes with the token", func(t *testing.T) {
		t.Parallel()
		if Fingerprint("https://api.example.com", "tok_b") == base {
			t.Fatal("Fingerprint should change when the token changes")
		}
	})

	t.Run("changes with the base URL", func(t *testing.T) {
		t.Parallel()
		if Fingerprint("https://other.example.com", "tok_a") == base {
			t.Fatal("Fingerprint should change when the base URL changes")
		}
	})
}
