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

// procTreeSubprocessCmd builds the self-exec of this test binary in
// subprocess mode. Credentials and API URL are threaded through the real
// env vars credentials.Load reads (OCEL_ACCESS_TOKEN, OCEL_API_URL) rather
// than a test seam, precisely so nothing here can quietly bypass the
// production RunE.
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
// real `ocel run` process — not a cancelled context — and checks its
// worker's grandchild dies with it.
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

// TestProcessTreeTTYForegroundHandoff drives `ocel run` on a real pty and
// runs a fixture that both reads stdin and calls tcsetattr (stty), the two
// operations that stop on SIGTTIN/SIGTTOU when a new process group is left
// as a background group of the controlling terminal. A stall here — the
// fixture never producing its output — is exactly the "glitching shell"
// regression: the child stops instead of running.
func TestProcessTreeTTYForegroundHandoff(t *testing.T) {
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
			t.Fatalf("fixture output = %q, never contained %q — the child likely stopped on SIGTTIN/SIGTTOU instead of running", readOut(), want)
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
