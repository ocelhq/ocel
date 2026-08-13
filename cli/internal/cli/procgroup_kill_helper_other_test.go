//go:build !unix

package cli

func pidAlive(pid int) bool {
	return false
}
