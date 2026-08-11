package lockfile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreate(t *testing.T) {
	t.Parallel()

	t.Run("a created lockfile reads back the address that was written", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		if err := Create(root, "127.0.0.1:54321"); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := Read(root)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got != "127.0.0.1:54321" {
			t.Fatalf("Read = %q, want %q", got, "127.0.0.1:54321")
		}
	})

	t.Run("a second create fails with an exist error and keeps the winner", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		if err := Create(root, "127.0.0.1:1"); err != nil {
			t.Fatalf("first Create: %v", err)
		}

		if err := Create(root, "127.0.0.1:2"); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("second Create err = %v, want an exist error", err)
		}

		got, err := Read(root)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got != "127.0.0.1:1" {
			t.Fatalf("Read = %q, want the first writer's address %q", got, "127.0.0.1:1")
		}
	})

	t.Run("the lockfile is readable only by its owner", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		if err := Create(root, "127.0.0.1:1"); err != nil {
			t.Fatalf("Create: %v", err)
		}

		path, err := Path(root)
		if err != nil {
			t.Fatalf("Path: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("lockfile mode = %v, want nothing readable or writable outside the owner", perm)
		}
	})
}

func TestRead(t *testing.T) {
	t.Parallel()

	t.Run("no lockfile reads as a not-exist error", func(t *testing.T) {
		t.Parallel()

		if _, err := Read(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Read err = %v, want a not-exist error", err)
		}
	})
}

func TestRemove(t *testing.T) {
	t.Parallel()

	t.Run("a removed lockfile reads as a not-exist error", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		if err := Create(root, "127.0.0.1:1"); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := Remove(root); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if _, err := Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Read after Remove err = %v, want a not-exist error", err)
		}
	})

	t.Run("removing a lockfile that was never created is not an error", func(t *testing.T) {
		t.Parallel()

		if err := Remove(t.TempDir()); err != nil {
			t.Fatalf("Remove on nonexistent lockfile: %v", err)
		}
	})
}

func TestPath(t *testing.T) {
	t.Parallel()

	t.Run("two project roots do not share one lockfile", func(t *testing.T) {
		t.Parallel()

		parent := t.TempDir()
		a, err := Path(filepath.Join(parent, "clone-a"))
		if err != nil {
			t.Fatalf("Path: %v", err)
		}
		b, err := Path(filepath.Join(parent, "clone-b"))
		if err != nil {
			t.Fatalf("Path: %v", err)
		}
		if a == b {
			t.Fatalf("Path(clone-a) == Path(clone-b) == %q, want distinct paths", a)
		}
	})

	t.Run("equivalent spellings of one root agree", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		canonical, err := Path(root)
		if err != nil {
			t.Fatalf("Path: %v", err)
		}
		roundabout, err := Path(filepath.Join(root, "sub", ".."))
		if err != nil {
			t.Fatalf("Path: %v", err)
		}
		if canonical != roundabout {
			t.Fatalf("Path(%q) = %q, Path(%q/sub/..) = %q, want them equal", root, canonical, root, roundabout)
		}
	})

	t.Run("the key is a flat name in the lock directory", func(t *testing.T) {
		t.Parallel()

		path, err := Path(t.TempDir())
		if err != nil {
			t.Fatalf("Path: %v", err)
		}
		dir, err := lockDir()
		if err != nil {
			t.Fatalf("lockDir: %v", err)
		}
		if filepath.Dir(path) != dir {
			t.Fatalf("Path = %q, want a file directly in %q", path, dir)
		}
	})
}

func TestLockDir(t *testing.T) {
	t.Parallel()

	t.Run("the lock directory sits under this user's own cache directory", func(t *testing.T) {
		t.Parallel()

		dir, err := lockDir()
		if err != nil {
			t.Fatalf("lockDir: %v", err)
		}
		cache, err := os.UserCacheDir()
		if err != nil {
			t.Fatalf("UserCacheDir: %v", err)
		}
		if !strings.HasPrefix(dir, cache+string(filepath.Separator)) {
			t.Fatalf("lockDir = %q, want a path inside %q — a directory shared with other users is one they can point at any address", dir, cache)
		}
	})

	t.Run("the lock directory is reachable only by its owner", func(t *testing.T) {
		t.Parallel()

		dir, err := lockDir()
		if err != nil {
			t.Fatalf("lockDir: %v", err)
		}
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm&0o077 != 0 {
			t.Fatalf("lock directory mode = %v, want nothing reachable outside the owner", perm)
		}
	})
}

func uniqueRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() { _ = Remove(root) })
	return root
}
