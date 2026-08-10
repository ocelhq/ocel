package election

import (
	"net"
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/lockfile"
)

func TestElect_NoLockfile_BecomesLeader(t *testing.T) {
	result, err := Elect(t.TempDir())
	if err != nil {
		t.Fatalf("Elect: %v", err)
	}
	if result.Role != Leader {
		t.Fatalf("Role = %v, want Leader", result.Role)
	}
}

func TestElect_LiveLock_BecomesFollower(t *testing.T) {
	root := t.TempDir()

	addr := liveAddr(t)
	if err := lockfile.Create(root, addr); err != nil {
		t.Fatalf("lockfile.Create: %v", err)
	}
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	result, err := Elect(root)
	if err != nil {
		t.Fatalf("Elect: %v", err)
	}
	if result.Role != Follower {
		t.Fatalf("Role = %v, want Follower", result.Role)
	}
	if result.LeaderAddr != addr {
		t.Fatalf("LeaderAddr = %q, want %q", result.LeaderAddr, addr)
	}

	if _, err := lockfile.Read(root); err != nil {
		t.Fatalf("lockfile.Read after Elect: %v", err)
	}
}

func TestElect_DeadLock_ReclaimsAndBecomesLeader(t *testing.T) {
	root := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	if err := lockfile.Create(root, addr); err != nil {
		t.Fatalf("lockfile.Create: %v", err)
	}
	t.Cleanup(func() { _ = lockfile.Remove(root) })

	result, err := Elect(root)
	if err != nil {
		t.Fatalf("Elect: %v", err)
	}
	if result.Role != Leader {
		t.Fatalf("Role = %v, want Leader", result.Role)
	}

	if _, err := lockfile.Read(root); !os.IsNotExist(err) {
		t.Fatalf("lockfile.Read after reclaim err = %v, want os.ErrNotExist", err)
	}
}

func TestElect_LiveLeaderInAnotherRoot_StillBecomesLeader(t *testing.T) {
	elsewhere, here := t.TempDir(), t.TempDir()

	if err := lockfile.Create(elsewhere, liveAddr(t)); err != nil {
		t.Fatalf("lockfile.Create: %v", err)
	}
	t.Cleanup(func() { _ = lockfile.Remove(elsewhere) })

	result, err := Elect(here)
	if err != nil {
		t.Fatalf("Elect: %v", err)
	}
	if result.Role != Leader {
		t.Fatalf("Role = %v, want Leader (a leader in %q must not be inherited by %q)", result.Role, elsewhere, here)
	}
}

func liveAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}
