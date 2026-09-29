//go:build unix

package childprocesstest

import "syscall"

func IsAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
