package leader

import (
	"errors"
	"io/fs"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/pkg/localrpc"
)

func TestFind(t *testing.T) {
	t.Parallel()

	t.Run("a record whose address answers is found as the leader", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		leader := answeringLeader(t)
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

	t.Run("a record whose address answers without accepting its token finds no leader", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		stranger := answeringLeader(t)
		if err := writeRecord(root, Leader{Address: stranger.Address, Token: "another-session"}); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}

		if _, found, err := Find(root); err != nil || found {
			t.Fatalf("Find found = %v, err = %v, want no leader: a reused port is not the leader that recorded it", found, err)
		}
	})

	t.Run("a record whose address accepts connections but speaks no HTTP finds no leader", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		if err := writeRecord(root, Leader{Address: silentAddress(t), Token: "app-token"}); err != nil {
			t.Fatalf("writeRecord: %v", err)
		}

		if _, found, err := Find(root); err != nil || found {
			t.Fatalf("Find found = %v, err = %v, want no leader", found, err)
		}
	})

	t.Run("a record with no token finds no leader", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		writeBareAddress(t, root, answeringLeader(t).Address)

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
		if err := writeRecord(elsewhere, answeringLeader(t)); err != nil {
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
		running := answeringLeader(t)
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
		writeBareAddress(t, root, answeringLeader(t).Address)

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
				claimants[i] = answeringLeader(t)
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
		first := answeringLeader(t)
		if err := Claim(root, first); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if err := Release(root, first.Token); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if _, err := Read(root); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Read after Release err = %v, want a not-exist error", err)
		}
		if err := Claim(root, answeringLeader(t)); err != nil {
			t.Fatalf("Claim after Release: %v", err)
		}
	})

	t.Run("a leader that stopped answering and releases late leaves its successor's record", func(t *testing.T) {
		t.Parallel()

		root := uniqueRoot(t)
		first := Leader{Address: deadAddress(t), Token: "first"}
		if err := Claim(root, first); err != nil {
			t.Fatalf("Claim: %v", err)
		}
		second := answeringLeader(t)
		if err := Claim(root, second); err != nil {
			t.Fatalf("Claim over a leader that stopped answering: %v", err)
		}

		if err := Release(root, first.Token); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if got, err := Read(root); err != nil || got != second {
			t.Fatalf("record after the first leader's release = %+v, %v, want the second leader %+v", got, err, second)
		}
		if err := Claim(root, answeringLeader(t)); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("third Claim err = %v, want ErrAlreadyRunning while the second leader answers", err)
		}
	})
}

func answeringLeader(t *testing.T) Leader {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	leader := Leader{Address: ln.Addr().String(), Token: localrpc.NewSessionToken()}
	mux := http.NewServeMux()
	mux.Handle("/env", localrpc.LoopbackGuard(leader.Address, leader.Token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})))
	server := &http.Server{Handler: mux}
	go server.Serve(ln)
	t.Cleanup(func() { _ = server.Close() })
	return leader
}

func silentAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
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
