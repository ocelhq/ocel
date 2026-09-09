package election

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"time"

	"github.com/ocelhq/ocel/cli/internal/devlock"
)

type Role int

const (
	Leader Role = iota
	Follower
)

func (r Role) String() string {
	switch r {
	case Leader:
		return "leader"
	case Follower:
		return "follower"
	default:
		return fmt.Sprintf("Role(%d)", int(r))
	}
}

var ErrLost = errors.New("another process claimed leadership first")

type Result struct {
	Role       Role
	LeaderAddr string

	root string
}

const dialTimeout = 500 * time.Millisecond

func Elect(root string) (Result, error) {
	addr, err := devlock.Read(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Result{Role: Leader, root: root}, nil
		}
		return Result{}, fmt.Errorf("read leader lockfile: %w", err)
	}

	if conn, dialErr := net.DialTimeout("tcp", addr, dialTimeout); dialErr == nil {
		conn.Close()
		return Result{Role: Follower, LeaderAddr: addr, root: root}, nil
	}

	if err := devlock.Remove(root); err != nil {
		return Result{}, fmt.Errorf("reclaim stale leader lockfile: %w", err)
	}
	return Result{Role: Leader, root: root}, nil
}

func (r Result) Claim(addr string) error {
	if r.Role != Leader || r.root == "" {
		return ErrLost
	}
	if err := devlock.Create(r.root, addr); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrLost
		}
		return fmt.Errorf("write leader lockfile: %w", err)
	}
	return nil
}

func (r Result) Release() error {
	if r.Role != Leader || r.root == "" {
		return nil
	}
	return devlock.Remove(r.root)
}
