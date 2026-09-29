package leader

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRecord(t *testing.T) {
	t.Parallel()

	t.Run("a written record reads back the leader that was written", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		leader := Leader{Address: "127.0.0.1:54321", Token: "app-token"}
		if err := writeRecord(root, leader); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}

		got, err := Read(root)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got != leader {
			t.Fatalf("Read = %+v, want %+v", got, leader)
		}
	})

	t.Run("a second write fails with an exist error and keeps the first record", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		if err := writeRecord(root, Leader{Address: "127.0.0.1:1", Token: "first"}); err != nil {
			t.Fatalf("first writeRecord: %v", err)
		}

		if err := writeRecord(root, Leader{Address: "127.0.0.1:2", Token: "second"}); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("second writeRecord err = %v, want an exist error", err)
		}

		got, err := Read(root)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got.Address != "127.0.0.1:1" || got.Token != "first" {
			t.Fatalf("Read = %+v, want the first writer's record", got)
		}
	})

	t.Run("a record read while it is being written is either absent or whole", func(t *testing.T) {
		t.Parallel()

		for range 200 {
			root := uniqueRoot(t)
			written := make(chan error, 1)
			go func() { written <- writeRecord(root, Leader{Address: "127.0.0.1:1", Token: "app-token"}) }()
			for {
				_, err := Read(root)
				if errors.Is(err, ErrMalformed) {
					t.Fatalf("Read during writeRecord = %v, want the record absent or whole", err)
				}
				if err == nil {
					break
				}
			}
			if err := <-written; err != nil {
				t.Fatalf("writeRecord: %v", err)
			}
		}
	})

	t.Run("the record is readable only by its owner", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		if err := writeRecord(root, Leader{Address: "127.0.0.1:1", Token: "app-token"}); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}

		path, err := recordPath(root)
		if err != nil {
			t.Fatalf("recordPath: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("record mode = %v, want nothing readable or writable outside the owner", perm)
		}
	})
}

func TestRead(t *testing.T) {
	t.Parallel()

	t.Run("no record reads as a not-exist error", func(t *testing.T) {
		t.Parallel()

		if _, err := Read(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Read err = %v, want a not-exist error", err)
		}
	})

	t.Run("a record that has an address and no token is malformed", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		writeBareAddress(t, root, "127.0.0.1:1")

		if _, err := Read(root); !errors.Is(err, ErrMalformed) {
			t.Fatalf("Read err = %v, want ErrMalformed", err)
		}
	})
}

func TestRelease(t *testing.T) {
	t.Parallel()

	t.Run("a released record reads as a not-exist error", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		if err := writeRecord(root, Leader{Address: "127.0.0.1:1", Token: "app-token"}); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}
		if err := Release(root, "app-token"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if _, err := Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Read after Release err = %v, want a not-exist error", err)
		}
	})

	t.Run("releasing with another leader's token leaves that leader's record", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		recorded := Leader{Address: "127.0.0.1:1", Token: "app-token"}
		if err := writeRecord(root, recorded); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}
		if err := Release(root, "an-earlier-leader"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if got, err := Read(root); err != nil || got != recorded {
			t.Fatalf("Read after a stranger's Release = %+v, %v, want %+v untouched", got, err, recorded)
		}
	})

	t.Run("releasing a record that was never written is not an error", func(t *testing.T) {
		t.Parallel()

		if err := Release(t.TempDir(), "app-token"); err != nil {
			t.Fatalf("Release on a missing record: %v", err)
		}
	})
}

func TestRecordPath(t *testing.T) {
	t.Parallel()

	t.Run("two project roots do not share one record", func(t *testing.T) {
		t.Parallel()

		parent := t.TempDir()
		a, err := recordPath(filepath.Join(parent, "clone-a"))
		if err != nil {
			t.Fatalf("recordPath: %v", err)
		}
		b, err := recordPath(filepath.Join(parent, "clone-b"))
		if err != nil {
			t.Fatalf("recordPath: %v", err)
		}
		if a == b {
			t.Fatalf("recordPath(clone-a) == recordPath(clone-b) == %q, want distinct paths", a)
		}
	})

	t.Run("equivalent spellings of one root agree", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		canonical, err := recordPath(root)
		if err != nil {
			t.Fatalf("recordPath: %v", err)
		}
		roundabout, err := recordPath(filepath.Join(root, "sub", ".."))
		if err != nil {
			t.Fatalf("recordPath: %v", err)
		}
		if canonical != roundabout {
			t.Fatalf("recordPath(%q) = %q, recordPath(%q/sub/..) = %q, want them equal", root, canonical, root, roundabout)
		}
	})

	t.Run("the record is a flat name in the record directory", func(t *testing.T) {
		t.Parallel()

		path, err := recordPath(t.TempDir())
		if err != nil {
			t.Fatalf("recordPath: %v", err)
		}
		dir, err := recordDir()
		if err != nil {
			t.Fatalf("recordDir: %v", err)
		}
		if filepath.Dir(path) != dir {
			t.Fatalf("recordPath = %q, want a file directly in %q", path, dir)
		}
	})
}

func TestRecordDir(t *testing.T) {
	t.Parallel()

	t.Run("the record directory sits under this user's own cache directory", func(t *testing.T) {
		t.Parallel()

		dir, err := recordDir()
		if err != nil {
			t.Fatalf("recordDir: %v", err)
		}
		cache, err := os.UserCacheDir()
		if err != nil {
			t.Fatalf("UserCacheDir: %v", err)
		}
		if !strings.HasPrefix(dir, cache+string(filepath.Separator)) {
			t.Fatalf("recordDir = %q, want a path inside %q — a directory shared with other users is one they can point at any address", dir, cache)
		}
	})

	t.Run("the record directory is reachable only by its owner", func(t *testing.T) {
		t.Parallel()

		dir, err := recordDir()
		if err != nil {
			t.Fatalf("recordDir: %v", err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("record directory mode = %v, want nothing reachable outside the owner", perm)
		}
	})
}

func uniqueRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() {
		if leader, err := Read(root); err == nil {
			_ = Release(root, leader.Token)
		}
	})
	return root
}

func writeBareAddress(t *testing.T, root, address string) {
	t.Helper()
	path, err := recordPath(root)
	if err != nil {
		t.Fatalf("recordPath: %v", err)
	}
	if err := os.WriteFile(path, []byte(address+"\n"), 0o600); err != nil {
		t.Fatalf("write a bare address: %v", err)
	}
}
