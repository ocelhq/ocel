//go:build unix

package providerprocess

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProviderExitsWhenTheCLIThatStartedItDies(t *testing.T) {
	t.Parallel()

	for _, sig := range []syscall.Signal{syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			t.Parallel()

			cli := exec.Command(os.Args[0])
			cli.Env = append(os.Environ(), fakeCLIEnvVar+"=1")
			cli.Stderr = os.Stderr
			stdout, err := cli.StdoutPipe()
			if err != nil {
				t.Fatalf("attach the fake CLI's stdout: %v", err)
			}
			if err := cli.Start(); err != nil {
				t.Fatalf("start the fake CLI: %v", err)
			}
			t.Cleanup(func() { _ = cli.Process.Kill() })

			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil {
				t.Fatalf("the fake CLI never reported its provider's pid: %v", err)
			}
			providerPid, err := strconv.Atoi(strings.TrimSpace(line))
			if err != nil {
				t.Fatalf("the fake CLI reported %q, want a pid", line)
			}
			t.Cleanup(func() { _ = syscall.Kill(-providerPid, syscall.SIGKILL) })

			if err := cli.Process.Signal(sig); err != nil {
				t.Fatalf("signal the fake CLI: %v", err)
			}
			_ = cli.Wait()

			deadline := time.Now().Add(5 * time.Second)
			for isRunning(providerPid) {
				if time.Now().After(deadline) {
					t.Fatalf("provider %d is still running 5s after its CLI died of %s, want it to exit with its CLI", providerPid, sig)
				}
				time.Sleep(20 * time.Millisecond)
			}
		})
	}
}

func isRunning(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	return !strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}
