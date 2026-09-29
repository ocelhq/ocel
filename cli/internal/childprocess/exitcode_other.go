//go:build !unix

package childprocess

import "os/exec"

func ExitCode(err *exec.ExitError) int {
	return err.ExitCode()
}
