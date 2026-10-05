//go:build unix

package enginetest

import "path/filepath"

func sweepingAlone() (release func()) {
	dir, err := slotDir()
	if err != nil {
		return func() {}
	}
	held, err := lockFile(filepath.Join(dir, "sweep.lock"), 0)
	if err != nil {
		return func() {}
	}
	return func() { held.Close() }
}
