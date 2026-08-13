//go:build unix

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// The tests in this file drive `ocel run` through a real, separate OS
// process, executing rootCmd via Execute() exactly as ocel/main.go does —
// not by calling runRun in-process with a hand-built context. That is the
// gap that let both process-tree blockers ship (see #247): a test that
// cancels a context directly cannot notice that runCmd's RunE never wired
// installInterruptHandler up to that context in the first place, because
// the test supplies the wiring itself instead of exercising it. The
// subprocess is this same test binary, re-executed with procTreeModeEnvVar
// set — the same self-exec trick deploy_fakeprovider_test.go already uses
// for a fake provider binary.
const procTreeModeEnvVar = "OCEL_TEST_PROCTREE_MODE"

const procTreeArgsSep = "\x1f"

func runProcessTreeSubprocess() int {
	if root := os.Getenv("OCEL_TEST_PROCTREE_ROOT"); root != "" {
		if err := os.Chdir(root); err != nil {
			fmt.Fprintln(os.Stderr, "process tree subprocess: chdir:", err)
			return 2
		}
	}

	argv := append([]string{"run", "--"}, strings.Split(os.Getenv("OCEL_TEST_PROCTREE_ARGS"), procTreeArgsSep)...)
	rootCmd.SetArgs(argv)

	err := Execute()
	if err == nil {
		return 0
	}
	var exitErr *ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	fmt.Fprintln(os.Stderr, "process tree subprocess error:", err)
	return 1
}

func procTreeSubprocessCmd(t *testing.T, root, apiURL string, appArgs []string) *exec.Cmd {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary path: %v", err)
	}

	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(),
		procTreeModeEnvVar+"=1",
		"OCEL_TEST_PROCTREE_ROOT="+root,
		"OCEL_TEST_PROCTREE_ARGS="+strings.Join(appArgs, procTreeArgsSep),
		"OCEL_ACCESS_TOKEN=tok",
		"OCEL_API_URL="+apiURL,
	)
	return cmd
}

func setUpProcTreeFixtureProject(t *testing.T) (root, apiURL string) {
	t.Helper()
	resolveServer := newFakeResolveServer(t)
	t.Cleanup(resolveServer.Close)

	root = t.TempDir()
	writeFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
	writeLink(t, root, resolveServer.URL, testProjectID(t))
	writeFile(t, filepath.Join(root, "ocel", "main.ts"), declareResourceScript("main"))
	return root, resolveServer.URL
}

// TestProcessTreeRealSIGINTKillsTheWholeTree sends an actual SIGINT to a
// real `ocel run` process's direct pid — not a cancelled context, and not
// group-wide — over a non-tty stdin. That puts the app child on the
// non-terminal path (its own process group), so this pins that path's
// existing, already-correct behaviour: a single signal to the CLI reaches
// the app's whole group.
func TestProcessTreeRealSIGINTKillsTheWholeTree(t *testing.T) {
	root, apiURL := setUpProcTreeFixtureProject(t)
	appArgs, startedPath, pidPath := fixtureWorkerTree(t, root, "sigint")

	cmd := procTreeSubprocessCmd(t, root, apiURL, appArgs)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}

	waitForFile(t, startedPath)

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("subprocess did not exit after a real SIGINT (want the app's group SIGTERMed well within the %s graceful window); stderr:\n%s", gracefulShutdownWindow, stderr.String())
	}

	waitProcessDead(t, pidPath)
}

// TestProcessTreeSetsidNonControllingTTYStillStarts is BLOCKER 2's
// reproduction: the CLI process is its own session leader (as `setsid ocel
// run` would make it) with a tty on stdin that it never acquired as its
// controlling terminal. The old NewForeground/SysProcAttr.Foreground path
// issued TIOCSPGRP on that fd between fork and exec, which returns ENOTTY
// there and aborted Start() outright — `dev`/`run` refused to launch the
// app at all. Since the redesign never touches the terminal on the tty
// path, Start() no longer cares whether the tty is controlling.
func TestProcessTreeSetsidNonControllingTTYStillStarts(t *testing.T) {
	root, apiURL := setUpProcTreeFixtureProject(t)
	appArgs, startedPath, pidPath := fixtureWorkerTree(t, root, "setsid-noctty")

	ptmx, ttySlave, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer ptmx.Close()

	cmd := procTreeSubprocessCmd(t, root, apiURL, appArgs)
	cmd.Stdin = ttySlave
	cmd.Stdout = ttySlave
	cmd.Stderr = ttySlave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v (want Start to succeed even though this tty is not the subprocess's controlling terminal)", err)
	}
	ttySlave.Close()

	waitForFile(t, startedPath)

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case <-waitDone:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("subprocess did not exit after a real SIGINT")
	}

	waitProcessDead(t, pidPath)
}

// TestProcessTreeOrphanedGroupTTYPassthrough drives `ocel run` on a real
// pty with the subprocess made a session leader that also acquires that
// pty as its controlling terminal (Setsid+Setctty) — an orphaned process
// group. This is the configuration process_tree_signal_test.go used to
// rely on for the old foreground-handoff tests, and per #247's own
// analysis it structurally cannot exercise either blocker: an orphaned
// group's tcsetpgrp fails with EIO/ENOTTY rather than raising SIGTTOU
// (blocker 1), and Setctty means Start() never hits blocker 2's ENOTTY
// either. It stays here only as a smoke check that a plain read and a
// tcsetattr (stty) still work on this path — which they trivially do now,
// since the tty path no longer touches the process group at all.
func TestProcessTreeOrphanedGroupTTYPassthrough(t *testing.T) {
	root, apiURL := setUpProcTreeFixtureProject(t)

	appArgs := []string{"sh", "-c", "read line; echo got:$line; stty raw -echo; echo raw-set; stty sane; echo done"}

	ptmx, ttySlave, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer ptmx.Close()

	cmd := procTreeSubprocessCmd(t, root, apiURL, appArgs)
	cmd.Stdin = ttySlave
	cmd.Stdout = ttySlave
	cmd.Stderr = ttySlave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	ttySlave.Close()

	var mu sync.Mutex
	var out strings.Builder
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				out.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	if _, err := ptmx.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write to pty: %v", err)
	}

	readOut := func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}

	for _, want := range []string{"got:hello", "raw-set", "done"} {
		if !waitFor(func() bool { return strings.Contains(readOut(), want) }, 10*time.Second) {
			t.Fatalf("fixture output = %q, never contained %q", readOut(), want)
		}
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		if err != nil {
			t.Fatalf("subprocess exited with error: %v; output:\n%s", err, readOut())
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("subprocess did not exit; output:\n%s", readOut())
	}

	before := readOut()
	time.Sleep(300 * time.Millisecond)
	if after := readOut(); after != before {
		t.Fatalf("pty received output after the CLI exited: %q", strings.TrimPrefix(after, before))
	}
}

// procTreeSessionHarnessEnvVar, when set on a re-exec of this test binary,
// makes it act as an interactive shell would: become the pty's session
// leader and controlling-terminal holder, start the real `ocel` subprocess
// as a new, distinct process group within that same session (Setpgid, no
// Setsid), hand that group the terminal's foreground (TIOCSPGRP) exactly
// as a shell does for a foreground job, then wait for it. That keeps the
// harness itself alive in the session the whole time, so the `ocel`
// subprocess's group is never orphaned — the configuration the old
// pty tests missed, and the one BLOCKER 1 (RestoreForeground's
// SIGTTOU) actually reproduces in.
const procTreeSessionHarnessEnvVar = "OCEL_TEST_PROCTREE_SESSION_HARNESS"

func runProcessTreeSessionHarness() int {
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "session harness: resolve self:", err)
		return 2
	}

	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, procTreeSessionHarnessEnvVar+"=") {
			continue
		}
		env = append(env, kv)
	}

	inner := exec.Command(self)
	inner.Env = env
	inner.Stdin = os.Stdin
	inner.Stdout = os.Stdout
	inner.Stderr = os.Stderr
	inner.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := inner.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "session harness: start inner:", err)
		return 2
	}

	if err := unix.IoctlSetPointerInt(0, unix.TIOCSPGRP, inner.Process.Pid); err != nil {
		fmt.Fprintln(os.Stderr, "session harness: tcsetpgrp:", err)
		_ = inner.Process.Kill()
		_, _ = inner.Process.Wait()
		return 2
	}

	err = inner.Wait()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fmt.Fprintln(os.Stderr, "session harness: wait inner:", err)
	return 1
}

// procTreeSessionCmd starts the session harness on a real pty and returns
// it already running, along with the pty master so the test can drive it
// like a real terminal (write bytes that the line discipline turns into
// signals). The harness's own exit code — read back via cmd.Wait() — is
// the inner `ocel` process's exit code, forwarded verbatim.
func procTreeSessionCmd(t *testing.T, root, apiURL string, appArgs []string) (cmd *exec.Cmd, ptmx *os.File) {
	t.Helper()
	ptmx, ttySlave, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	t.Cleanup(func() { ptmx.Close() })

	cmd = procTreeSubprocessCmd(t, root, apiURL, appArgs)
	cmd.Env = append(cmd.Env, procTreeSessionHarnessEnvVar+"=1")
	cmd.Stdin = ttySlave
	cmd.Stdout = ttySlave
	cmd.Stderr = ttySlave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start session harness: %v", err)
	}
	ttySlave.Close()
	return cmd, ptmx
}

const ctrlC = 0x03

// TestProcessTreeNonOrphanedCtrlCReachesCLIAndApp sends a real SIGINT —
// via the pty's line discipline, exactly as a terminal driver does on
// Ctrl-C, to the whole foreground process group — against a non-orphaned
// group (see procTreeSessionCmd). It pins the behaviour the maintainer's
// decision trades for: since the app child shares the CLI's own group
// instead of getting a background one of its own, the signal reaches the
// CLI (which tears down through installInterruptHandler's graceful path,
// not its force-kill one) and the app's 3-level tree (which dies via the
// CLI's explicit descendant walk, since every level here ignores the
// kernel-delivered SIGINT itself). The app dying by SIGTERM rather than
// exiting on its own makes exec.ExitError.ExitCode() report -1, which
// waitExitError forwards as the CLI's own exit code — that is a distinct,
// pre-existing quirk of signal-killed children and not what this test is
// about, so it only asserts the code is not interruptExitCode: that is
// what would tell us installInterruptHandler's force path fired instead of
// letting ctx cancellation return normally.
func TestProcessTreeNonOrphanedCtrlCReachesCLIAndApp(t *testing.T) {
	root, apiURL := setUpProcTreeFixtureProject(t)
	appArgs, startedPath, leafPidPath := fixtureDeepWorkerTree(t, root, "nonorphan-ctrlc")

	cmd, ptmx := procTreeSessionCmd(t, root, apiURL, appArgs)

	waitForFile(t, startedPath)

	if _, err := ptmx.Write([]byte{ctrlC}); err != nil {
		t.Fatalf("write ctrl-c to pty: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == interruptExitCode {
			t.Fatalf("CLI exit code = %d, want anything but interruptExitCode: a single Ctrl-C should take the graceful path, not the force-kill one", exitErr.ExitCode())
		} else if err != nil && !errors.As(err, &exitErr) {
			t.Fatalf("session harness wait error = %v, want nil or an *exec.ExitError", err)
		}
	case <-time.After(gracefulShutdownWindow + 5*time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("CLI did not exit within the graceful window after a single Ctrl-C")
	}

	waitProcessDead(t, leafPidPath)
}

// fixtureStubbornWorkerTree returns app args for a single process that
// ignores both SIGINT and SIGTERM, so it can only be reaped by SIGKILL —
// giving the CLI's escalation goroutine's appChildGracePeriod window (and
// so a second Ctrl-C during it) something real to race against instead of
// the app dying instantly.
func fixtureStubbornWorkerTree(t *testing.T, root, name string) (appArgs []string, startedPath, pidPath string) {
	t.Helper()
	startedPath = filepath.Join(root, name+".started")
	pidPath = filepath.Join(root, name+".workerpid")
	appArgs = []string{"sh", "-c", "trap '' INT TERM; echo $$ > " + pidPath + "; touch " + startedPath + "; while true; do sleep 1; done"}
	return appArgs, startedPath, pidPath
}

// TestProcessTreeNonOrphanedSecondCtrlCIsFatal proves #245's "the second
// Ctrl-C is always fatal" guarantee survives the redesign for `dev`/`run`
// on a real, non-orphaned tty: a stubborn app (ignores INT and TERM) forces
// the CLI into its grace period, a second Ctrl-C lands inside that window,
// and the CLI force-exits with interruptExitCode well before the app's own
// SIGKILL-only death would have ended things on its own.
func TestProcessTreeNonOrphanedSecondCtrlCIsFatal(t *testing.T) {
	root, apiURL := setUpProcTreeFixtureProject(t)
	appArgs, startedPath, pidPath := fixtureStubbornWorkerTree(t, root, "nonorphan-second-ctrlc")

	cmd, ptmx := procTreeSessionCmd(t, root, apiURL, appArgs)

	waitForFile(t, startedPath)

	if _, err := ptmx.Write([]byte{ctrlC}); err != nil {
		t.Fatalf("write first ctrl-c to pty: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := ptmx.Write([]byte{ctrlC}); err != nil {
		t.Fatalf("write second ctrl-c to pty: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		exitErr := &exec.ExitError{}
		if !errors.As(err, &exitErr) {
			t.Fatalf("session harness wait error = %v, want an *exec.ExitError carrying the CLI's exit code", err)
		}
		if code := exitErr.ExitCode(); code != interruptExitCode {
			t.Fatalf("CLI exit code = %d, want %d (forced exit on the second Ctrl-C)", code, interruptExitCode)
		}
	case <-time.After(appChildGracePeriod + 3*time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("CLI did not force-exit promptly after a second Ctrl-C")
	}

	waitProcessDead(t, pidPath)
}
