// Package election decides whether an `ocel dev` process becomes the leader
// or a follower for a working tree, based on the lockfile recorded by
// internal/lockfile.
package election

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/ocelhq/ocel/cli/internal/lockfile"
)

// Role is the outcome of Elect for the current process.
type Role int

const (
	// Leader means no live lock was found (either none existed, or a dead
	// one was reclaimed): this process must bind a listener and write its
	// address to the lockfile itself.
	Leader Role = iota
	// Follower means a live leader was found at Result.LeaderAddr.
	Follower
)

func (r Role) String() string {
	if r == Leader {
		return "Leader"
	}
	return "Follower"
}

// Result is the outcome of Elect.
type Result struct {
	Role Role
	// LeaderAddr is the live leader's address. Only set when Role == Follower.
	LeaderAddr string
}

// dialTimeout bounds how long Elect waits to confirm a recorded lockfile
// address is still reachable before concluding it's dead.
var dialTimeout = 500 * time.Millisecond

// Elect determines this process's role for the project rooted at root:
//   - no lockfile -> Leader.
//   - lockfile with a reachable address -> Follower.
//   - lockfile with an unreachable address (a prior leader crashed without
//     cleaning up) -> the stale lockfile is removed and this process
//     becomes Leader. Followers never self-promote; only Elect reclaims.
//
// The root is the unit of isolation, so election needs no cloud state and can
// run before any is resolved.
//
// On becoming Leader, the caller is responsible for binding its own
// listener and recording its address with lockfile.Create. That create is
// exclusive: if it fails with os.ErrExist, a concurrent process won the
// election first, and the caller should re-run Elect to join it.
func Elect(root string) (Result, error) {
	addr, err := lockfile.Read(root)
	if err != nil {
		if os.IsNotExist(err) {
			return Result{Role: Leader}, nil
		}
		return Result{}, fmt.Errorf("read leader lockfile: %w", err)
	}

	if conn, dialErr := net.DialTimeout("tcp", addr, dialTimeout); dialErr == nil {
		conn.Close()
		return Result{Role: Follower, LeaderAddr: addr}, nil
	}

	if err := lockfile.Remove(root); err != nil {
		return Result{}, fmt.Errorf("reclaim stale leader lockfile: %w", err)
	}
	return Result{Role: Leader}, nil
}
