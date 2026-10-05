//go:build unix

package enginetest

import (
	"os"
	"path/filepath"
	"syscall"
)

func sweepingAlone() (release func()) {
	held, err := os.OpenFile(filepath.Join(os.TempDir(), rootPrefix+"sweep.lock"), os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return func() {}
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		held.Close()
		return func() {}
	}
	return func() { held.Close() }
}
