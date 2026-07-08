package election

import (
	"net"
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/lockfile"
)

func TestElect_NoLockfile_BecomesLeader(t *testing.T) {
	projectID := uniqueProjectID(t)

	result, err := Elect(projectID)
	if err != nil {
		t.Fatalf("Elect: %v", err)
	}
	if result.Role != Leader {
		t.Fatalf("Role = %v, want Leader", result.Role)
	}
}

func TestElect_LiveLock_BecomesFollower(t *testing.T) {
	projectID := uniqueProjectID(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().String()
	if err := lockfile.Create(projectID, addr); err != nil {
		t.Fatalf("lockfile.Create: %v", err)
	}
	t.Cleanup(func() { _ = lockfile.Remove(projectID) })

	result, err := Elect(projectID)
	if err != nil {
		t.Fatalf("Elect: %v", err)
	}
	if result.Role != Follower {
		t.Fatalf("Role = %v, want Follower", result.Role)
	}
	if result.LeaderAddr != addr {
		t.Fatalf("LeaderAddr = %q, want %q", result.LeaderAddr, addr)
	}

	// The live lockfile must be left in place for other followers.
	if _, err := lockfile.Read(projectID); err != nil {
		t.Fatalf("lockfile.Read after Elect: %v", err)
	}
}

func TestElect_DeadLock_ReclaimsAndBecomesLeader(t *testing.T) {
	projectID := uniqueProjectID(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing is listening anymore: the lock is dead.

	if err := lockfile.Create(projectID, addr); err != nil {
		t.Fatalf("lockfile.Create: %v", err)
	}
	t.Cleanup(func() { _ = lockfile.Remove(projectID) })

	result, err := Elect(projectID)
	if err != nil {
		t.Fatalf("Elect: %v", err)
	}
	if result.Role != Leader {
		t.Fatalf("Role = %v, want Leader", result.Role)
	}

	if _, err := lockfile.Read(projectID); !os.IsNotExist(err) {
		t.Fatalf("lockfile.Read after reclaim err = %v, want os.ErrNotExist", err)
	}
}

func uniqueProjectID(t *testing.T) string {
	t.Helper()
	return "test-" + t.Name()
}
