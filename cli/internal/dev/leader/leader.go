package leader

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"time"
)

type Leader struct {
	Address string
	Token   string
}

var ErrAlreadyRunning = errors.New("another ocel dev already leads this project")

const dialTimeout = 500 * time.Millisecond

func Find(root string) (Leader, bool, error) {
	leader, err := Read(root)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, ErrMalformed):
		return Leader{}, false, nil
	case err != nil:
		return Leader{}, false, fmt.Errorf("read the dev leader record: %w", err)
	}
	return leader, isAnswering(leader), nil
}

func Claim(root string, leader Leader) error {
	lock, err := lockRecord(root)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()

	current, err := Read(root)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case errors.Is(err, ErrMalformed):
		if err := removeRecord(root); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("read the dev leader record: %w", err)
	case isAnswering(current):
		return ErrAlreadyRunning
	default:
		if err := removeRecord(root); err != nil {
			return err
		}
	}

	if err := writeRecord(root, leader); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrAlreadyRunning
		}
		return err
	}
	return nil
}

func Release(root string) error {
	return removeRecord(root)
}

func isAnswering(leader Leader) bool {
	conn, err := net.DialTimeout("tcp", leader.Address, dialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
