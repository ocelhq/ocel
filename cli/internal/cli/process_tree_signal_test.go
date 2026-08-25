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

	"github.com/ocelhq/ocel/cli/internal/exitsig"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
)

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
	var exitErr *exitsig.ExitError
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
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
	writeLink(t, root, resolveServer.URL, testProjectID(t))
	clitest.WriteFile(t, filepath.Join(root, "ocel", "main.ts"), declareResourceScript("main"))
	return root, resolveServer.URL
}

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

func drainPTY(ptmx *os.File) func() string {
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
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}
}

func TestProcessTreeNonOrphanedCtrlCReachesCLIAndApp(t *testing.T) {
	root, apiURL := setUpProcTreeFixtureProject(t)
	appArgs, startedPath, leafPidPath := fixtureDeepWorkerTree(t, root, "nonorphan-ctrlc")

	cmd, ptmx := procTreeSessionCmd(t, root, apiURL, appArgs)
	tty := drainPTY(ptmx)

	waitForFile(t, startedPath)

	if _, err := ptmx.Write([]byte{ctrlC}); err != nil {
		t.Fatalf("write ctrl-c to pty: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != exitsig.InterruptCode {
			t.Fatalf("CLI exit error = %v, want exit code %d after a Ctrl-C", err, exitsig.InterruptCode)
		}
		if out := tty(); strings.Contains(out, "did not finish") || strings.Contains(out, "Interrupted again") {
			t.Fatalf("tty output = %q, want a single Ctrl-C to take the graceful path, not the force-kill one", out)
		}
	case <-time.After(gracefulShutdownWindow + 5*time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("CLI did not exit within the graceful window after a single Ctrl-C")
	}

	waitProcessDead(t, leafPidPath)
}

func fixtureStubbornWorkerTree(t *testing.T, root, name string) (appArgs []string, startedPath, pidPath string) {
	t.Helper()
	startedPath = filepath.Join(root, name+".started")
	pidPath = filepath.Join(root, name+".workerpid")
	appArgs = []string{"sh", "-c", "trap '' INT TERM; echo $$ > " + pidPath + "; touch " + startedPath + "; while true; do sleep 1; done"}
	return appArgs, startedPath, pidPath
}

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
		if code := exitErr.ExitCode(); code != exitsig.InterruptCode {
			t.Fatalf("CLI exit code = %d, want %d (forced exit on the second Ctrl-C)", code, exitsig.InterruptCode)
		}
	case <-time.After(appChildGracePeriod + 3*time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("CLI did not force-exit promptly after a second Ctrl-C")
	}

	waitProcessDead(t, pidPath)
}
