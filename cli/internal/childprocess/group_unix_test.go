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
)

func TestSignallingAGroupThatIsAlreadyGone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		signal func(*exec.Cmd) error
	}{
		{name: "killing a group that is already gone reports the process done", signal: KillGroup},
		{name: "terminating a group that is already gone reports the process done", signal: TerminateGroup},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.Command("sleep", "5")
			SetOwnGroup(cmd)
			if err := cmd.Start(); err != nil {
				t.Fatalf("start: %v", err)
			}
			if err := KillGroup(cmd); err != nil {
				t.Fatalf("first KillGroup: %v", err)
			}
			_ = cmd.Wait()

			deadline := time.Now().Add(2 * time.Second)
			for {
				err := tc.signal(cmd)
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

func TestKillAllKillsEveryProcessOfAGroupStillRunning(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	group, err := StartGroup(exec.CommandContext(context.Background(), "sh", "-c", `sleep 60 & echo $! > "$0"; wait`, pidFile))
	if err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	var pid int
	for deadline := time.Now().Add(5 * time.Second); pid == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the group never started its sleep")
		}
		if said, err := os.ReadFile(pidFile); err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(said)))
		}
	}

	KillAll()

	waited := make(chan error, 1)
	go func() { waited <- group.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the group still runs 5s after KillAll")
	}
	for deadline := time.Now().Add(2 * time.Second); childprocesstest.IsAlive(pid); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the group's sleep %d still runs after KillAll", pid)
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
