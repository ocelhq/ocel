package lockfile

import (
	"os"
	"testing"
)

func TestRead_NoLockfile_ReturnsErrNotExist(t *testing.T) {
	_, err := Read(uniqueProjectID(t))
	if !os.IsNotExist(err) {
		t.Fatalf("Read err = %v, want os.ErrNotExist", err)
	}
}

func TestWriteThenRead_RoundTrips(t *testing.T) {
	projectID := uniqueProjectID(t)
	t.Cleanup(func() { _ = Remove(projectID) })

	if err := Write(projectID, "127.0.0.1:54321"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(projectID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got != "127.0.0.1:54321" {
		t.Fatalf("Read = %q, want %q", got, "127.0.0.1:54321")
	}
}

func TestRemove_ThenRead_ReturnsErrNotExist(t *testing.T) {
	projectID := uniqueProjectID(t)

	if err := Write(projectID, "127.0.0.1:1"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := Remove(projectID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := Read(projectID); !os.IsNotExist(err) {
		t.Fatalf("Read after Remove err = %v, want os.ErrNotExist", err)
	}
}

func TestRemove_NoLockfile_DoesNotError(t *testing.T) {
	if err := Remove(uniqueProjectID(t)); err != nil {
		t.Fatalf("Remove on nonexistent lockfile: %v", err)
	}
}

func TestPath_DiffersByProjectID(t *testing.T) {
	a, err := Path("proj-a")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	b, err := Path("proj-b")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if a == b {
		t.Fatalf("Path(proj-a) == Path(proj-b) == %q, want distinct paths", a)
	}
}

func uniqueProjectID(t *testing.T) string {
	t.Helper()
	return "test-" + t.Name()
}
