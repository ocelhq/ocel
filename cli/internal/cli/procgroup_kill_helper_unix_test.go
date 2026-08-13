//go:build unix

package cli

import "syscall"

func pidAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
