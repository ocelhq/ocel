//go:build unix

package sourcearchive_test

import "syscall"

func mkfifo(file string) error {
	return syscall.Mkfifo(file, 0o644)
}
