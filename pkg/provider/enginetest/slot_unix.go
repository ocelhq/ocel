//go:build unix

package enginetest

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"syscall"
)

func leaseSlot(dir string, count int) (int, *os.File, error) {
	for slot := range count {
		held, err := lockSlot(dir, slot, syscall.LOCK_NB)
		if err == nil {
			return slot, held, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return 0, nil, err
		}
	}
	slot := rand.IntN(count)
	held, err := lockSlot(dir, slot, 0)
	if err != nil {
		return 0, nil, err
	}
	return slot, held, nil
}

func lockSlot(dir string, slot, how int) (*os.File, error) {
	return lockFile(filepath.Join(dir, fmt.Sprintf("slot-%d.lock", slot)), how)
}

func lockFile(at string, how int) (*os.File, error) {
	held, err := os.OpenFile(at, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX|how); err != nil {
		held.Close()
		return nil, err
	}
	return held, nil
}
