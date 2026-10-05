package childprocess

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

const detachedFlags = windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS

func StartDetached(newCmd func() *exec.Cmd) error {
	err := startReleased(withCreationFlags(newCmd(), detachedFlags|windows.CREATE_BREAKAWAY_FROM_JOB))
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return startReleased(withCreationFlags(newCmd(), detachedFlags))
	}
	return err
}

func withCreationFlags(cmd *exec.Cmd, flags uint32) *exec.Cmd {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= flags
	return cmd
}
