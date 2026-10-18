//go:build !windows

package lifecycle

import (
	"os/exec"
)

func newShellCommand(line string) *exec.Cmd {
	return exec.Command("sh", "-c", line)
}
