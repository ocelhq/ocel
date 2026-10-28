//go:build unix

package childprocess

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess/childprocesstest"
	"github.com/ocelhq/ocel/pkg/tracing"
)

func TestSignallingAGroupThatIsAlreadyGone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		signal func(*processGroup, *exec.Cmd) error
	}{
		{name: "killing a group that is already gone reports the process done", signal: (*processGroup).kill},
		{name: "terminating a group that is already gone reports the process done", signal: (*processGroup).terminate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.Command("sleep", "5")
			group, err := newProcessGroup(cmd, true)
			if err != nil {
				t.Fatalf("newProcessGroup: %v", err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			if err := group.kill(cmd); err != nil {
				t.Fatalf("first KillGroup: %v", err)
			}
			_ = cmd.Wait()

			deadline := time.Now().Add(2 * time.Second)
			for {
				err := tc.signal(group, cmd)
				if errors.Is(err, os.ErrProcessDone) {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("signal on an already-gone group = %v, want os.ErrProcessDone so os/exec does not invent a context.Canceled", err)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

func TestKillAllKillsEveryProcessOfAChildStillRunning(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	child, err := Start(context.Background(), tracing.Command(context.Background(), "sh", "-c", `sleep 60 & echo $! > "$0"; wait`, pidFile), Options{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the child never started its sleep")
		}
		if said, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(said)))
		}
	}

	KillAll()

	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the child still runs 5s after KillAll")
	}
	for deadline := time.Now().Add(2 * time.Second); childprocesstest.IsAlive(pid); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the child's sleep %d still runs after KillAll", pid)
		}
	}
}

func TestSignallingATreeThatIsAlreadyGoneReportsTheProcessDone(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run fixture: %v", err)
	}

	if err := terminateTree(cmd); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("terminateTree on a finished process = %v, want os.ErrProcessDone so os/exec does not invent a context.Canceled", err)
	}
}
