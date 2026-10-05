//go:build !unix && !windows

package childprocess

import "os/exec"

func StartDetached(newCmd func() *exec.Cmd) error {
	return startReleased(newCmd())
}
