//go:build !unix

package cli

import (
	"os/exec"

	"github.com/ocelhq/ocel/cli/internal/procgroup"
)

func terminateAppTree(cmd *exec.Cmd) error {
	return procgroup.Terminate(cmd)
}

func killAppTree(cmd *exec.Cmd) error {
	return procgroup.Kill(cmd)
}

func appExitCode(err *exec.ExitError) int {
	return err.ExitCode()
}
