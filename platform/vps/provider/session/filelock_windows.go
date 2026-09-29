package session

import (
	"math"
	"os"

	"golang.org/x/sys/windows"
)

func lockExclusive(file *os.File) error {
	return windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, math.MaxUint32, math.MaxUint32, &windows.Overlapped{})
}
