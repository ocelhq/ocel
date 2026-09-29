package leader

import (
	"errors"
	"io/fs"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFind(t *testing.T) {
	t.Parallel()

	t.Run("a record whose address answers is found as the leader", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		leader := Leader{Address: liveAddress(t), Token: "app-token"}
		if err := writeRecord(root, leader); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}

		got, found, err := Find(root)
		if err != nil {
			t.Fatalf("Find: %v", err)
		}
		if !found || got != leader {
			t.Fatalf("Find = %+v, %v, want %+v found", got, found, leader)
		}
	})

	t.Run("looking for a leader leaves a record whose address is dead in place", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		leader := Leader{Address: deadAddress(t), Token: "app-token"}
		if err := writeRecord(root, leader); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}

		if _, found, err := Find(root); err != nil || found {
			t.Fatalf("Find found = %v, err = %v, want no leader and no error", found, err)
		}
		if got, err := Read(root); err != nil || got != leader {
			t.Fatalf("Read after Find = %+v, %v, want the record untouched", got, err)
		}
	})

	t.Run("a record with no token finds no leader", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		writeBareAddress(t, root, liveAddress(t))

		if _, found, err := Find(root); err != nil || found {
			t.Fatalf("Find found = %v, err = %v, want no leader: a record no follower can authenticate with is worth nothing", found, err)
		}
	})

	t.Run("no record finds no leader", func(t *testing.T) {
		t.Parallel()

		if _, found, err := Find(uniqueRoot(t)); err != nil || found {
			t.Fatalf("Find found = %v, err = %v, want no leader and no error", found, err)
		}
	})

	t.Run("a live leader in another root is not found from this one", func(t *testing.T) {
		t.Parallel()

		elsewhere, here := uniqueRoot(t), uniqueRoot(t)
		if err := writeRecord(elsewhere, Leader{Address: liveAddress(t), Token: "app-token"}); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}

		if _, found, err := Find(here); err != nil || found {
			t.Fatalf("Find(%q) found = %v, err = %v, want the leader in %q not inherited", here, found, err, elsewhere)
		}
	})
}

func TestClaim(t *testing.T) {
	t.Parallel()

	t.Run("the first claim records the leader's own address", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		leader := Leader{Address: "127.0.0.1:4242", Token: "app-token"}
		if err := Claim(root, leader); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		got, err := Read(root)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got != leader {
			t.Fatalf("record contains %+v, want the claiming leader %+v", got, leader)
		}
	})

	t.Run("a claim over a leader that answers is refused and leaves its record alone", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		running := Leader{Address: liveAddress(t), Token: "app-token"}
		if err := Claim(root, running); err != nil {
			t.Fatalf("first Claim: %v", err)
		}

		if err := Claim(root, Leader{Address: "127.0.0.1:9", Token: "usurper"}); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("second Claim err = %v, want ErrAlreadyRunning", err)
		}
		if got, err := Read(root); err != nil || got != running {
			t.Fatalf("record = %+v, %v, want the running leader %+v", got, err, running)
		}
	})

	t.Run("a claim over a record whose address is dead replaces it", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		if err := writeRecord(root, Leader{Address: deadAddress(t), Token: "dead"}); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}

		leader := Leader{Address: "127.0.0.1:4242", Token: "app-token"}
		if err := Claim(root, leader); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if got, err := Read(root); err != nil || got != leader {
			t.Fatalf("record = %+v, %v, want the new leader %+v", got, err, leader)
		}
	})

	t.Run("a claim over a record with no token replaces it", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		writeBareAddress(t, root, liveAddress(t))

		leader := Leader{Address: "127.0.0.1:4242", Token: "app-token"}
		if err := Claim(root, leader); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if got, err := Read(root); err != nil || got != leader {
			t.Fatalf("record = %+v, %v, want the new leader %+v", got, err, leader)
		}
	})

	t.Run("of many processes claiming at once over a dead leader's record, exactly one leads", func(t *testing.T) {
		t.Parallel()

		for range 100 {
			root := uniqueRoot(t)
			claimants := make([]Leader, 8)
			for i := range claimants {
				claimants[i] = Leader{Address: liveAddress(t), Token: "app-token"}
			}
			if err := writeRecord(root, Leader{Address: deadAddress(t), Token: "dead"}); err != nil {
				t.Fatalf("writeRecord: %v", err)
			}

			var (
				wg      sync.WaitGroup
				claimed atomic.Int32
			)
			for _, claimant := range claimants {
				wg.Go(func() {
					err := Claim(root, claimant)
					if errors.Is(err, ErrAlreadyRunning) {
						return
					}
					if err != nil {
						t.Errorf("Claim: %v", err)
						return
					}
					claimed.Add(1)
				})
			}
			wg.Wait()
			if got := claimed.Load(); got != 1 {
				t.Fatalf("%d processes claimed the project, want exactly one leader", got)
			}
		}
	})
}

func TestReleaseAfterClaim(t *testing.T) {
	t.Parallel()

	t.Run("release drops the claim so the next claim leads", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		if err := Claim(root, Leader{Address: liveAddress(t), Token: "first"}); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if err := Release(root); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if _, err := Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Read after Release err = %v, want a not-exist error", err)
		}
		if err := Claim(root, Leader{Address: liveAddress(t), Token: "second"}); err != nil {
			t.Fatalf("Claim after Release: %v", err)
		}
	})
}

func liveAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String()
}

func deadAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	address := ln.Addr().String()
	ln.Close()
	return address
}
