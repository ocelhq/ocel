//go:build unix

package cli

import (
	"errors"
	"os/exec"
	"syscall"
)

func terminateAppTree(cmd *exec.Cmd) error {
	return signalAppTree(cmd, syscall.SIGTERM)
}

func killAppTree(cmd *exec.Cmd) error {
	return signalAppTree(cmd, syscall.SIGKILL)
}

func signalAppTree(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	pids := append([]int{pid}, descendantPIDs(pid)...)

	var firstErr error
	for _, p := range pids {
		err := syscall.Kill(p, sig)
		if err == nil || errors.Is(err, syscall.ESRCH) {
			continue
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
