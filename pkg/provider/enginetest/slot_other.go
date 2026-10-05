//go:build !unix

package enginetest

import (
	"errors"
	"os"
)

func leaseSlot(string, int) (int, *os.File, error) {
	return 0, nil, errors.New("a network slot is held by an flock, which this platform does not have")
}
