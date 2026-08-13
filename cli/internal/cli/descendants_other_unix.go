//go:build unix && !linux

package cli

func descendantPIDs(pid int) []int {
	return nil
}
