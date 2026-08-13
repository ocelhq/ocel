//go:build !unix

package cli

import (
	"os/exec"

	"github.com/ocelhq/ocel/cli/internal/procgroup"
)

func setNewProcessGroup(cmd *exec.Cmd) {
	procgroup.New(cmd)
}

func killProcessGroup(cmd *exec.Cmd) error {
	return procgroup.Kill(cmd)
}
