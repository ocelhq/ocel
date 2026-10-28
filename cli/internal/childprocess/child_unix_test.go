//go:build unix

package childprocess

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess/childprocesstest"
	"github.com/ocelhq/ocel/pkg/tracing"
)

func readPID(t *testing.T, path string) int {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if said, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(said))); err == nil {
				return pid
			}
		}
	}
	t.Fatalf("no pid was ever written to %s", path)
	return 0
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); childprocesstest.IsAlive(pid); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still runs", pid)
		}
	}
}

func TestStoppingAChildAsksItToExitBeforeKillingIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	marker, ready := filepath.Join(dir, "cleaned-up"), filepath.Join(dir, "ready")
	cmd := tracing.Command(context.Background(), "sh", "-c", `trap 'touch "$0"; exit 0' TERM; touch "$1"; while :; do sleep 0.05; done`, marker, ready)
	child, err := Start(context.Background(), cmd, Options{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	childprocesstest.WaitForFile(t, ready)

	child.Stop()

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the child never ran its cleanup (%v), want Stop to ask it to exit before killing it", err)
	}
}

func TestStoppingAChildThatIgnoresTheRequestKillsItOnceTheGraceRunsOut(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	cmd := tracing.Command(context.Background(), "sh", "-c", `trap '' TERM; touch "$0"; while :; do sleep 0.05; done`, ready)
	child, err := Start(context.Background(), cmd, Options{Grace: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	childprocesstest.WaitForFile(t, ready)

	stopped := make(chan struct{})
	go func() {
		child.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop still waits on a child that ignores SIGTERM, want it killed once the grace runs out")
	}
	waitGone(t, cmd.Process.Pid)
}

func TestRunEndsTheChildsWholeGroupWhenItsContextEnds(t *testing.T) {
	t.Parallel()

	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	ctx, cancel := context.WithCancel(context.Background())
	cmd := tracing.Command(context.Background(), "sh", "-c", `sleep 60 & echo $! > "$0"; wait`, pidFile)
	ran := make(chan error, 1)
	go func() { ran <- Run(ctx, cmd) }()
	pid := readPID(t, pidFile)

	cancel()

	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("Run still waits 10s after its context ended")
	}
	waitGone(t, pid)
}

func TestStoppingAChildWhosePipeOutlivesItReturnsOnceItsReapRunsOut(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("needs setsid to start a grandchild outside the child's group")
	}

	dir := t.TempDir()
	ready, pidFile := filepath.Join(dir, "ready"), filepath.Join(dir, "grandchild.pid")
	cmd := tracing.Command(context.Background(), "sh", "-c", `setsid sh -c 'echo $$ > "$1"; exec sleep 30' sh "$1" & trap '' TERM; touch "$0"; while :; do sleep 0.05; done`, ready, pidFile)
	cmd.Stdout = &strings.Builder{}
	child, err := Start(context.Background(), cmd, Options{Grace: 1500 * time.Millisecond, Reap: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	childprocesstest.WaitForFile(t, ready)
	grandchild := readPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	start := time.Now()
	child.Stop()
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Errorf("Stop took %s on a child whose pipe a grandchild holds, want it to return once the reap runs out", elapsed)
	}
}
