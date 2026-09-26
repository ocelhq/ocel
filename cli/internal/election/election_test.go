package election

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/devlock"
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

	t.Run("a lockfile with no token is reclaimed and this process leads", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		path, err := devlock.Path(root)
		if err != nil {
			t.Fatalf("devlock.Path: %v", err)
		}
		if err := os.WriteFile(path, []byte(liveAddr(t)+"\n"), 0o600); err != nil {
			t.Fatalf("write a bare address: %v", err)
		}

		result := elect(t, root)
		if result.Role != Leader {
			t.Fatalf("Role = %v, want leader: a lock no follower can authenticate with is worth nothing", result.Role)
		}
		if _, err := devlock.Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("devlock.Read after reclaim err = %v, want a not-exist error", err)
		}
	})

	t.Run("a lockfile whose address answers makes this process a follower", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		addr := liveAddr(t)
		if err := devlock.Create(root, devlock.Lease{Addr: addr, Token: "app-token"}); err != nil {
			t.Fatalf("devlock.Create: %v", err)
		}

		result := elect(t, root)
		if result.Role != Follower {
			t.Fatalf("Role = %v, want follower", result.Role)
		}
		if result.Leader.Addr != addr || result.Leader.Token != "app-token" {
			t.Fatalf("Leader = %+v, want the lock's address %q and its token", result.Leader, addr)
		}
		if _, err := devlock.Read(root); err != nil {
			t.Fatalf("devlock.Read after Elect: %v", err)
		}
	})

	t.Run("a lockfile whose address is dead is reclaimed and this process leads", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		if err := devlock.Create(root, devlock.Lease{Addr: deadAddr(t), Token: "app-token"}); err != nil {
			t.Fatalf("devlock.Create: %v", err)
		}

		result := elect(t, root)
		if result.Role != Leader {
			t.Fatalf("Role = %v, want leader", result.Role)
		}
		if _, err := devlock.Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("devlock.Read after reclaim err = %v, want a not-exist error", err)
		}
	})

	t.Run("a live leader in another root is not inherited by this one", func(t *testing.T) {
		t.Parallel()

		elsewhere, here := root(t), root(t)
		if err := devlock.Create(elsewhere, devlock.Lease{Addr: liveAddr(t), Token: "app-token"}); err != nil {
			t.Fatalf("devlock.Create: %v", err)
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

		lease := devlock.Lease{Addr: "127.0.0.1:4242", Token: "app-token"}
		if err := result.Claim(lease); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		got, err := devlock.Read(root)
		if err != nil {
			t.Fatalf("devlock.Read: %v", err)
		}
		if got != lease {
			t.Fatalf("lockfile contains %+v, want the claiming leader's lease %+v", got, lease)
		}
	})

	t.Run("only one of two processes told they lead can claim the lock", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		first, second := elect(t, root), elect(t, root)
		if first.Role != Leader || second.Role != Leader {
			t.Fatalf("roles = %v and %v, want both to be told they lead — that is the race Claim exists to decide", first.Role, second.Role)
		}

		if err := first.Claim(devlock.Lease{Addr: "127.0.0.1:1", Token: "first"}); err != nil {
			t.Fatalf("first Claim: %v", err)
		}
		if err := second.Claim(devlock.Lease{Addr: "127.0.0.1:2", Token: "second"}); !errors.Is(err, ErrLost) {
			t.Fatalf("second Claim err = %v, want ErrLost", err)
		}

		got, err := devlock.Read(root)
		if err != nil {
			t.Fatalf("devlock.Read: %v", err)
		}
		if got.Addr != "127.0.0.1:1" {
			t.Fatalf("lockfile contains %+v, want the winner's address %q", got, "127.0.0.1:1")
		}
	})

	t.Run("a follower can never claim the lock", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		addr := liveAddr(t)
		if err := devlock.Create(root, devlock.Lease{Addr: addr, Token: "app-token"}); err != nil {
			t.Fatalf("devlock.Create: %v", err)
		}

		result := elect(t, root)
		if err := result.Claim(devlock.Lease{Addr: "127.0.0.1:9", Token: "usurper"}); !errors.Is(err, ErrLost) {
			t.Fatalf("Claim from a follower err = %v, want ErrLost", err)
		}
		got, err := devlock.Read(root)
		if err != nil {
			t.Fatalf("devlock.Read: %v", err)
		}
		if got.Addr != addr {
			t.Fatalf("lockfile contains %+v, want the current leader's address %q", got, addr)
		}
	})

	t.Run("a zero result claims nothing", func(t *testing.T) {
		t.Parallel()

		if err := (Result{}).Claim(devlock.Lease{Addr: "127.0.0.1:9", Token: "app-token"}); !errors.Is(err, ErrLost) {
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
		if err := result.Claim(devlock.Lease{Addr: liveAddr(t), Token: "app-token"}); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if err := result.Release(); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if _, err := devlock.Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("devlock.Read after Release err = %v, want a not-exist error", err)
		}
	})

	t.Run("a follower releasing leaves the current leader's claim alone", func(t *testing.T) {
		t.Parallel()

		root := root(t)
		addr := liveAddr(t)
		if err := devlock.Create(root, devlock.Lease{Addr: addr, Token: "app-token"}); err != nil {
			t.Fatalf("devlock.Create: %v", err)
		}

		result := elect(t, root)
		if err := result.Release(); err != nil {
			t.Fatalf("Release: %v", err)
		}
		got, err := devlock.Read(root)
		if err != nil {
			t.Fatalf("devlock.Read after a follower released: %v", err)
		}
		if got.Addr != addr {
			t.Fatalf("lockfile contains %+v, want the current leader's address %q", got, addr)
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
	t.Cleanup(func() { _ = devlock.Remove(root) })
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
