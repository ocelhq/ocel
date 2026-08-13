//go:build unix

package procgroup

import (
	"os/exec"
	"testing"
	"time"
)

func TestKillOnAlreadyGoneGroupReturnsNil(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sleep", "5")
	New(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := Kill(cmd); err != nil {
		t.Fatalf("first Kill() error = %v, want nil", err)
	}
	_ = cmd.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := Kill(cmd)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Kill() on an already-gone group = %v, want nil (ESRCH mapped away)", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestTerminateOnAlreadyGoneGroupReturnsNil(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("sleep", "5")
	New(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := Kill(cmd); err != nil {
		t.Fatalf("Kill() error = %v, want nil", err)
	}
	_ = cmd.Wait()

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := Terminate(cmd)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Terminate() on an already-gone group = %v, want nil (ESRCH mapped away)", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
