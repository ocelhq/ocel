//go:build unix

package childprocess

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
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
