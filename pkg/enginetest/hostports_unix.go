//go:build unix

package enginetest

import (
	"errors"
	"os"
	"syscall"
)

func lockIfFree(at string) (held *os.File, free bool, err error) {
	held, err = lockFile(at, syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil, false, nil
	}
	return held, err == nil, err
}
