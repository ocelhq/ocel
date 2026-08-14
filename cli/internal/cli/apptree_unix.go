//go:build unix

package cli

import (
	"errors"
	"os"
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
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid
	pids := append([]int{pid}, descendantPIDs(pid)...)

	var firstErr error
	delivered := false
	for _, p := range pids {
		err := syscall.Kill(p, sig)
		switch {
		case err == nil:
			delivered = true
		case errors.Is(err, syscall.ESRCH):
		case firstErr == nil:
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if !delivered {
		return os.ErrProcessDone
	}
	return nil
}

func appExitCode(err *exec.ExitError) int {
	if status, ok := err.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return err.ExitCode()
}
