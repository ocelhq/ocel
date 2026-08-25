//go:build linux || darwin

package cli

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const stubbornRawModeFixture = `
stty -icanon -echo -icrnl
trap '' INT TERM
echo raw-set
while true; do sleep 1; done
`

func termiosOf(t *testing.T, ptmx *os.File) *unix.Termios {
	t.Helper()
	term, err := unix.IoctlGetTermios(int(ptmx.Fd()), getTermiosRequest)
	if err != nil {
		t.Fatalf("IoctlGetTermios: %v", err)
	}
	return term
}

func waitForRawMode(t *testing.T, ptmx *os.File) {
	t.Helper()
	if !waitFor(func() bool {
		term, err := unix.IoctlGetTermios(int(ptmx.Fd()), getTermiosRequest)
		return err == nil && (term.Lflag&unix.ICANON == 0 || term.Lflag&unix.ECHO == 0)
	}, 10*time.Second) {
		t.Fatalf("app never appeared to disable icanon/echo")
	}
}

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
