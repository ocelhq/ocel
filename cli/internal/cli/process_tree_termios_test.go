//go:build unix

package cli

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// stubbornRawModeFixture is a POSIX shell that mirrors an interactive dev
// server reading raw keypresses: it clears ICANON/ECHO but leaves ISIG set,
// same as well-behaved raw-mode tools do so Ctrl-C keeps generating SIGINT
// for the whole foreground group instead of silently swallowing it. It then
// ignores both INT and TERM itself, so the only way to remove it is the
// CLI's own SIGKILL — the shape that leaves the terminal raw unless the CLI
// restores it, since the child never gets a chance to.
const stubbornRawModeFixture = `
stty -icanon -echo -icrnl
trap '' INT TERM
echo raw-set
while true; do sleep 1; done
`

func termiosOf(t *testing.T, ptmx *os.File) *unix.Termios {
	t.Helper()
	term, err := unix.IoctlGetTermios(int(ptmx.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("IoctlGetTermios: %v", err)
	}
	return term
}

func waitForRawMode(t *testing.T, ptmx *os.File) {
	t.Helper()
	if !waitFor(func() bool {
		term, err := unix.IoctlGetTermios(int(ptmx.Fd()), unix.TCGETS)
		return err == nil && (term.Lflag&unix.ICANON == 0 || term.Lflag&unix.ECHO == 0)
	}, 10*time.Second) {
		t.Fatalf("app never appeared to disable icanon/echo")
	}
}

// TestProcessTreeTermiosRestoredAfterGraceKill pins the fix for the
// "glitching shell" gap in #247's design: since the app child now shares the
// CLI's own foreground group and controlling terminal (instead of a
// background group that could never touch it), a dev server that
// legitimately raw-modes the terminal can leave it that way if it is killed
// before it restores things itself. A single Ctrl-C reaches the whole
// foreground group by the kernel's own delivery, but stubbornRawModeFixture
// ignores it, so only the CLI's own escalation SIGKILL (after
// appChildGracePeriod) ever removes it — exactly the path that must restore
// the terminal on the child's behalf.
func TestProcessTreeTermiosRestoredAfterGraceKill(t *testing.T) {
	root, apiURL := setUpProcTreeFixtureProject(t)
	appArgs := []string{"sh", "-c", stubbornRawModeFixture}

	cmd, ptmx := procTreeSessionCmd(t, root, apiURL, appArgs)
	before := termiosOf(t, ptmx)
	waitForRawMode(t, ptmx)

	if _, err := ptmx.Write([]byte{ctrlC}); err != nil {
		t.Fatalf("write ctrl-c: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(gracefulShutdownWindow + 5*time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("CLI did not exit within the graceful window after the app was force-killed past its grace period")
	}

	after := termiosOf(t, ptmx)
	if *after != *before {
		t.Errorf("terminal state not restored after the app child was SIGKILLed past its grace period: before lflag=%#o, after lflag=%#o", before.Lflag, after.Lflag)
	}
}

// TestProcessTreeTermiosRestoredAfterSecondCtrlCForcedExit is the same gap
// on the interrupt handler's forced-exit path (see exit.go's
// interruptHandlerWithExit): a second Ctrl-C during the grace period calls
// forceKillEverything then os.Exit directly, with no time for a graceful
// unwind. killAllLiveAppChildren has to restore the terminal itself,
// synchronously, before that exit — waiting on the app child's own
// cmd.Wait() goroutine would race the process exiting out from under it.
func TestProcessTreeTermiosRestoredAfterSecondCtrlCForcedExit(t *testing.T) {
	root, apiURL := setUpProcTreeFixtureProject(t)
	appArgs := []string{"sh", "-c", stubbornRawModeFixture}

	cmd, ptmx := procTreeSessionCmd(t, root, apiURL, appArgs)
	before := termiosOf(t, ptmx)
	waitForRawMode(t, ptmx)

	if _, err := ptmx.Write([]byte{ctrlC}); err != nil {
		t.Fatalf("write first ctrl-c: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := ptmx.Write([]byte{ctrlC}); err != nil {
		t.Fatalf("write second ctrl-c: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(appChildGracePeriod + 5*time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("CLI did not force-exit promptly after a second Ctrl-C")
	}

	after := termiosOf(t, ptmx)
	if *after != *before {
		t.Errorf("terminal state not restored after the second-ctrl-c forced exit: before lflag=%#o, after lflag=%#o", before.Lflag, after.Lflag)
	}
}
