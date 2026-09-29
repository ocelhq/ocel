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
	Role   Role
	Leader devlock.Lease

	root string
}

const dialTimeout = 500 * time.Millisecond

func Elect(root string) (Result, error) {
	lock, err := devlock.Lock(root)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = lock.Unlock() }()

	lease, err := devlock.Read(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Result{Role: Leader, root: root}, nil
	case errors.Is(err, devlock.ErrMalformed):
	case err != nil:
		return Result{}, fmt.Errorf("read leader lockfile: %w", err)
	case isAnswering(lease):
		return Result{Role: Follower, Leader: lease, root: root}, nil
	}

	if err := devlock.Remove(root); err != nil {
		return Result{}, fmt.Errorf("reclaim stale leader lockfile: %w", err)
	}
	return Result{Role: Leader, root: root}, nil
}

func FindLeader(root string) (devlock.Lease, bool, error) {
	lease, err := devlock.Read(root)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, devlock.ErrMalformed):
		return devlock.Lease{}, false, nil
	case err != nil:
		return devlock.Lease{}, false, fmt.Errorf("read leader lockfile: %w", err)
	}
	return lease, isAnswering(lease), nil
}

func isAnswering(lease devlock.Lease) bool {
	conn, err := net.DialTimeout("tcp", lease.Addr, dialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func (r Result) Claim(lease devlock.Lease) error {
	if r.Role != Leader || r.root == "" {
		return ErrLost
	}
	lock, err := devlock.Lock(r.root)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	if err := devlock.Create(r.root, lease); err != nil {
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
