package secret

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var purpose = Purpose{Owner: "postgres", Noun: "password"}

func TestEnsure(t *testing.T) {
	t.Parallel()

	t.Run("a secret is generated once and read back after", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "secrets", "postgres-password")
		first, err := Ensure(path, purpose)
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		again, err := Ensure(path, purpose)
		if err != nil {
			t.Fatalf("second Ensure: %v", err)
		}
		if first == "" || again != first {
			t.Fatalf("Ensure = %q then %q, want one non-empty secret", first, again)
		}
	})

	t.Run("callers ensuring one secret at once all get the same value", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "postgres-password")
		got := make([]string, 8)
		var wg sync.WaitGroup
		for i := range got {
			wg.Go(func() {
				value, err := Ensure(path, purpose)
				if err != nil {
					t.Errorf("Ensure: %v", err)
				}
				got[i] = value
			})
		}
		wg.Wait()
		for _, value := range got {
			if value != got[0] {
				t.Fatalf("secrets = %v, want every caller to agree", got)
			}
		}
	})

	t.Run("the secret and its directory are reachable only by their owner", func(t *testing.T) {
		t.Parallel()

		dir := filepath.Join(t.TempDir(), "secrets")
		path := filepath.Join(dir, "postgres-password")
		if _, err := Ensure(path, purpose); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		for _, p := range []string{dir, path} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if perm := info.Mode().Perm(); perm&0o077 != 0 {
				t.Fatalf("%s mode = %v, want nothing reachable outside the owner", p, perm)
			}
		}
	})

	t.Run("an empty secret file is refused with the reset that starts over", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "postgres-password")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatalf("write an empty secret: %v", err)
		}
		_, err := Ensure(path, purpose)
		if err == nil || !strings.Contains(err.Error(), "contains no password") || !strings.Contains(err.Error(), "ocel dev --reset") {
			t.Fatalf("Ensure err = %v, want the empty file named with the reset that fixes it", err)
		}
	})
}
