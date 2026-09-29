//go:build !unix

package childprocesstest

func IsAlive(pid int) bool {
	return false
}
