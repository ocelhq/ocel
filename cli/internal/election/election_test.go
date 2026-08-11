package election

import (
	"errors"
	"io/fs"
	"net"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/lockfile"
)

func TestRoleString(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		role Role
		want string
	}{
		{name: "the leader renders as its own name", role: Leader, want: "leader"},
		{name: "the follower renders as its own name", role: Follower, want: "follower"},
		{name: "a value that is no role at all renders as itself, not as the follower", role: Role(97), want: "Role(97)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.role.String(); got != tc.want {
				t.Fatalf("Role(%d).String() = %q, want %q", int(tc.role), got, tc.want)
			}
		})
	}
}

func TestElect(t *testing.T) {
	t.Parallel()

	t.Run("no lockfile makes this process the leader", func(t *testing.T) {
		t.Parallel()

		result := elect(t, root(t))
		if result.Role != Leader {
			t.Fatalf("Role = %v, want leader", result.Role)
		}
	})

	t.Run("a lockfile whose address answers makes this process a follower", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		addr := liveAddr(t)
		if err := lockfile.Create(root, addr); err != nil {
			t.Fatalf("lockfile.Create: %v", err)
		}

		result := elect(t, root)
		if result.Role != Follower {
			t.Fatalf("Role = %v, want follower", result.Role)
		}
		if result.LeaderAddr != addr {
			t.Fatalf("LeaderAddr = %q, want %q", result.LeaderAddr, addr)
		}
		if _, err := lockfile.Read(root); err != nil {
			t.Fatalf("lockfile.Read after Elect: %v", err)
		}
	})

	t.Run("a lockfile whose address is dead is reclaimed and this process leads", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		if err := lockfile.Create(root, deadAddr(t)); err != nil {
			t.Fatalf("lockfile.Create: %v", err)
		}

		result := elect(t, root)
		if result.Role != Leader {
			t.Fatalf("Role = %v, want leader", result.Role)
		}
		if _, err := lockfile.Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("lockfile.Read after reclaim err = %v, want a not-exist error", err)
		}
	})

	t.Run("a live leader in another root is not inherited by this one", func(t *testing.T) {
		t.Parallel()

		elsewhere, here := root(t), root(t)
		if err := lockfile.Create(elsewhere, liveAddr(t)); err != nil {
			t.Fatalf("lockfile.Create: %v", err)
		}

		result := elect(t, here)
		if result.Role != Leader {
			t.Fatalf("Role = %v, want leader (a leader in %q must not be inherited by %q)", result.Role, elsewhere, here)
		}
	})
}

func TestResultClaim(t *testing.T) {
	t.Parallel()

	t.Run("the leader claims the lock and advertises its own address", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		result := elect(t, root)

		if err := result.Claim("127.0.0.1:4242"); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		got, err := lockfile.Read(root)
		if err != nil {
			t.Fatalf("lockfile.Read: %v", err)
		}
		if got != "127.0.0.1:4242" {
			t.Fatalf("lockfile holds %q, want the claiming leader's address %q", got, "127.0.0.1:4242")
		}
	})

	t.Run("only one of two processes told they lead can claim the lock", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		first, second := elect(t, root), elect(t, root)
		if first.Role != Leader || second.Role != Leader {
			t.Fatalf("roles = %v and %v, want both to be told they lead — that is the race Claim exists to settle", first.Role, second.Role)
		}

		if err := first.Claim("127.0.0.1:1"); err != nil {
			t.Fatalf("first Claim: %v", err)
		}
		if err := second.Claim("127.0.0.1:2"); !errors.Is(err, ErrLost) {
			t.Fatalf("second Claim err = %v, want ErrLost", err)
		}

		got, err := lockfile.Read(root)
		if err != nil {
			t.Fatalf("lockfile.Read: %v", err)
		}
		if got != "127.0.0.1:1" {
			t.Fatalf("lockfile holds %q, want the winner's address %q", got, "127.0.0.1:1")
		}
	})

	t.Run("a follower can never claim the lock", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		addr := liveAddr(t)
		if err := lockfile.Create(root, addr); err != nil {
			t.Fatalf("lockfile.Create: %v", err)
		}

		result := elect(t, root)
		if err := result.Claim("127.0.0.1:9"); !errors.Is(err, ErrLost) {
			t.Fatalf("Claim from a follower err = %v, want ErrLost", err)
		}
		got, err := lockfile.Read(root)
		if err != nil {
			t.Fatalf("lockfile.Read: %v", err)
		}
		if got != addr {
			t.Fatalf("lockfile holds %q, want the standing leader's address %q", got, addr)
		}
	})

	t.Run("a zero result claims nothing", func(t *testing.T) {
		t.Parallel()

		if err := (Result{}).Claim("127.0.0.1:9"); !errors.Is(err, ErrLost) {
			t.Fatalf("Claim on a result no election produced err = %v, want ErrLost", err)
		}
	})
}

func TestResultRelease(t *testing.T) {
	t.Parallel()

	t.Run("release drops the claim so the next election leads", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		result := elect(t, root)
		if err := result.Claim(liveAddr(t)); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if err := result.Release(); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if _, err := lockfile.Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("lockfile.Read after Release err = %v, want a not-exist error", err)
		}
	})

	t.Run("a follower releasing leaves the standing leader's claim alone", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		addr := liveAddr(t)
		if err := lockfile.Create(root, addr); err != nil {
			t.Fatalf("lockfile.Create: %v", err)
		}

		result := elect(t, root)
		if err := result.Release(); err != nil {
			t.Fatalf("Release: %v", err)
		}
		got, err := lockfile.Read(root)
		if err != nil {
			t.Fatalf("lockfile.Read after a follower released: %v", err)
		}
		if got != addr {
			t.Fatalf("lockfile holds %q, want the standing leader's address %q", got, addr)
		}
	})
}

func elect(t *testing.T, root string) Result {
	t.Helper()
	result, err := Elect(root)
	if err != nil {
		t.Fatalf("Elect: %v", err)
	}
	return result
}

func root(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Cleanup(func() { _ = lockfile.Remove(root) })
	return root
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

func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}
