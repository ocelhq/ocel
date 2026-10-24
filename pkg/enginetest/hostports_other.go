//go:build !unix

package enginetest

import (
	"errors"
	"os"
)

func lockIfFree(string) (*os.File, bool, error) {
	return nil, false, errors.New("a host port is held by an flock, which this platform does not have")
}
