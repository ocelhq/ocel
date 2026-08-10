package lockfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRead_NoLockfile_ReturnsErrNotExist(t *testing.T) {
	_, err := Read(t.TempDir())
	if !os.IsNotExist(err) {
		t.Fatalf("Read err = %v, want os.ErrNotExist", err)
	}
}

func TestCreateThenRead_RoundTrips(t *testing.T) {
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
}

func TestCreate_ExistingLockfile_FailsWithErrExistAndKeepsWinner(t *testing.T) {
	root := uniqueRoot(t)

	if err := Create(root, "127.0.0.1:1"); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	err := Create(root, "127.0.0.1:2")
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("second Create err = %v, want os.ErrExist", err)
	}

	got, err := Read(root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "127.0.0.1:1" {
		t.Fatalf("Read = %q, want the first writer's address %q", got, "127.0.0.1:1")
	}
}

func TestRemove_ThenRead_ReturnsErrNotExist(t *testing.T) {
	root := uniqueRoot(t)

	if err := Create(root, "127.0.0.1:1"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Remove(root); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := Read(root); !os.IsNotExist(err) {
		t.Fatalf("Read after Remove err = %v, want os.ErrNotExist", err)
	}
}

func TestRemove_NoLockfile_DoesNotError(t *testing.T) {
	if err := Remove(t.TempDir()); err != nil {
		t.Fatalf("Remove on nonexistent lockfile: %v", err)
	}
}

func TestPath_DiffersByProjectRoot(t *testing.T) {
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
}

func TestPath_EquivalentSpellingsOfOneRoot_Agree(t *testing.T) {
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
}

func TestPath_KeyIsAFlatNameInTheLockDirectory(t *testing.T) {
	root := t.TempDir()

	path, err := Path(root)
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
}

func uniqueRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() { _ = Remove(root) })
	return root
}
