//go:build unix

package root

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/childprocess/childprocesstest"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"golang.org/x/sys/unix"
)

type terminalOutput struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (o *terminalOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.Write(p)
}

func (o *terminalOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func TestCtrlCDuringALinkLeavesItsTranscriptWithNoLiveLineAndExitsInterrupted(t *testing.T) {
	stalled := func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }
	twoOrganizations := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"1","name":"Acme","slug":"acme"},{"id":"2","name":"Initech","slug":"initech"}]`)
	}
	for _, tc := range []struct {
		name    string
		console http.HandlerFunc
		shown   string
	}{
		{name: "while the console is loading", console: stalled, shown: "Loading your organizations"},
		{name: "at a prompt", console: twoOrganizations, shown: "Select an organization"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen, exited := linkOnATerminal(t, tc.console, tc.shown)

			var exitErr *exec.ExitError
			if !errors.As(exited, &exitErr) || exitErr.ExitCode() != 130 {
				t.Errorf("ocel link exited with %v, want status 130", exited)
			}
			_, afterResult, ok := strings.Cut(ansi.Strip(screen), "Link cancelled")
			if !ok {
				t.Fatalf("the terminal shows %q, want the run ended as cancelled", screen)
			}
			if strings.Contains(afterResult, "[check]") {
				t.Errorf("after the result the terminal shows %q, want no live line left drawn", afterResult)
			}
		})
	}
}

func linkOnATerminal(t *testing.T, console http.HandlerFunc, shown string) (screen string, exited error) {
	t.Helper()
	server := httptest.NewServer(console)
	t.Cleanup(server.Close)
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatalf("resolve test binary path: %v", err)
	}
	cmd := exec.Command(self)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		rootArgsEnvVar+"=link",
		"OCEL_ACCESS_TOKEN=tok",
		"OCEL_CONSOLE_URL="+server.URL,
		"TERM=xterm-256color",
	)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() { ptmx.Close() })
	var out terminalOutput
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(&out, ptmx)
		close(drained)
	}()

	if !waitFor(func() bool { return strings.Contains(out.String(), shown) }, 10*time.Second) {
		_ = cmd.Process.Kill()
		t.Fatalf("the terminal shows %q, want %q before Ctrl-C", out.String(), shown)
	}
	if _, err := ptmx.Write([]byte{3}); err != nil {
		t.Fatalf("type Ctrl-C: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case exited = <-done:
		select {
		case <-drained:
		case <-time.After(gracefulShutdownWindow / 2):
		}
	case <-time.After(gracefulShutdownWindow / 2):
		_ = cmd.Process.Kill()
		t.Fatalf("ocel link did not exit well within its %s shutdown window after Ctrl-C; the terminal shows %q", gracefulShutdownWindow, out.String())
	}
	return out.String(), exited
}

func waitFor(cond func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

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
	ocel := newCommand()
	ocel.root.SetArgs(argv)

	err := ocel.execute()
	if err == nil {
		return 0
	}
	var exitErr *exitcode.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.Code
	}
	fmt.Fprintln(os.Stderr, "process tree subprocess error:", err)
	return 1
}

func procTreeSubprocessCmd(t *testing.T, root string, appArgs []string) *exec.Cmd {
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
	)
	return cmd
}

func setUpProcTreeFixtureProject(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), `
export default { slug: "test-app" };
`)
	return root
}

func TestProcessTreeRealSIGINTKillsTheWholeTree(t *testing.T) {
	root := setUpProcTreeFixtureProject(t)
	appArgs, startedPath, pidPath := childprocesstest.WorkerTree(t, root, "sigint")

	cmd := procTreeSubprocessCmd(t, root, appArgs)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}

	childprocesstest.WaitForFile(t, startedPath)

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

	childprocesstest.WaitDead(t, pidPath)
}

func TestProcessTreeSetsidNonControllingTTYStillStarts(t *testing.T) {
	root := setUpProcTreeFixtureProject(t)
	appArgs, startedPath, pidPath := childprocesstest.WorkerTree(t, root, "setsid-noctty")

	ptmx, ttySlave, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer ptmx.Close()

	cmd := procTreeSubprocessCmd(t, root, appArgs)
	cmd.Stdin = ttySlave
	cmd.Stdout = ttySlave
	cmd.Stderr = ttySlave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v (want Start to succeed even though this tty is not the subprocess's controlling terminal)", err)
	}
	ttySlave.Close()

	childprocesstest.WaitForFile(t, startedPath)

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

	childprocesstest.WaitDead(t, pidPath)
}

func TestProcessTreeOrphanedGroupTTYPassthrough(t *testing.T) {
	root := setUpProcTreeFixtureProject(t)

	appArgs := []string{"sh", "-c", "read line; echo got:$line; stty raw -echo; echo raw-set; stty sane; echo done"}

	ptmx, ttySlave, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	defer ptmx.Close()

	cmd := procTreeSubprocessCmd(t, root, appArgs)
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

func procTreeSessionCmd(t *testing.T, root string, appArgs []string) (cmd *exec.Cmd, ptmx *os.File) {
	t.Helper()
	ptmx, ttySlave, err := pty.Open()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	t.Cleanup(func() { ptmx.Close() })

	cmd = procTreeSubprocessCmd(t, root, appArgs)
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
	root := setUpProcTreeFixtureProject(t)
	appArgs, startedPath, leafPidPath := childprocesstest.DeepWorkerTree(t, root, "nonorphan-ctrlc")

	cmd, ptmx := procTreeSessionCmd(t, root, appArgs)
	tty := drainPTY(ptmx)

	childprocesstest.WaitForFile(t, startedPath)

	if _, err := ptmx.Write([]byte{ctrlC}); err != nil {
		t.Fatalf("write ctrl-c to pty: %v", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case err := <-waitDone:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != exitcode.Interrupt {
			t.Fatalf("CLI exit error = %v, want exit code %d after a Ctrl-C", err, exitcode.Interrupt)
		}
		if out := tty(); strings.Contains(out, "did not finish") || strings.Contains(out, "Interrupted again") {
			t.Fatalf("tty output = %q, want a single Ctrl-C to take the graceful path, not the force-kill one", out)
		}
	case <-time.After(gracefulShutdownWindow + 5*time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("CLI did not exit within the graceful window after a single Ctrl-C")
	}

	childprocesstest.WaitDead(t, leafPidPath)
}

func fixtureStubbornWorkerTree(t *testing.T, root, name string) (appArgs []string, startedPath, pidPath string) {
	t.Helper()
	startedPath = filepath.Join(root, name+".started")
	pidPath = filepath.Join(root, name+".workerpid")
	appArgs = []string{"sh", "-c", "trap '' INT TERM; echo $$ > " + pidPath + "; touch " + startedPath + "; while true; do sleep 1; done"}
	return appArgs, startedPath, pidPath
}

func TestProcessTreeNonOrphanedSecondCtrlCIsFatal(t *testing.T) {
	root := setUpProcTreeFixtureProject(t)
	appArgs, startedPath, pidPath := fixtureStubbornWorkerTree(t, root, "nonorphan-second-ctrlc")

	cmd, ptmx := procTreeSessionCmd(t, root, appArgs)

	childprocesstest.WaitForFile(t, startedPath)

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
			t.Fatalf("session harness wait error = %v, want an *exec.ExitError with the CLI's exit code", err)
		}
		if code := exitErr.ExitCode(); code != exitcode.Interrupt {
			t.Fatalf("CLI exit code = %d, want %d (forced exit on the second Ctrl-C)", code, exitcode.Interrupt)
		}
	case <-time.After(childprocess.GracePeriod + 3*time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("CLI did not force-exit promptly after a second Ctrl-C")
	}

	childprocesstest.WaitDead(t, pidPath)
}
