package election

import (
	"fmt"
	"net"
	"os"
	"time"

	"github.com/ocelhq/ocel/cli/internal/lockfile"
)

type Role int

const (
	Leader Role = iota
	Follower
)

func (r Role) String() string {
	if r == Leader {
		return "Leader"
	}
	return "Follower"
}

type Result struct {
	Role       Role
	LeaderAddr string
}

var dialTimeout = 500 * time.Millisecond

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
