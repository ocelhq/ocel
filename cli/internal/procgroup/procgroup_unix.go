//go:build unix

package procgroup

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func newGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// newForegroundGroup uses SysProcAttr.Foreground rather than a manual
// tcsetpgrp() in this process after Start(): Foreground does the
// setpgid-then-TIOCSPGRP handoff in the child itself, between fork and
// exec, which is race-free by construction and needs no SIGTTOU
// suppression here — the naive alternative (setpgid in the parent, then
// tcsetpgrp from the parent once the pid is known) has a window where the
// child is already a background group before the parent gets to call
// tcsetpgrp, so a child that reads stdin immediately can still be stopped.
func newForegroundGroup(cmd *exec.Cmd, tty *os.File) {
	if tty == nil {
		newGroup(cmd)
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Foreground = true
	cmd.SysProcAttr.Ctty = int(tty.Fd())
}

func restoreForeground(tty *os.File) error {
	if tty == nil {
		return nil
	}
	return unix.IoctlSetPointerInt(int(tty.Fd()), unix.TIOCSPGRP, syscall.Getpgrp())
}

func terminate(cmd *exec.Cmd) error {
	return signalGroup(cmd, syscall.SIGTERM)
}

func kill(cmd *exec.Cmd) error {
	return signalGroup(cmd, syscall.SIGKILL)
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, sig)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
