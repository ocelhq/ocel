package childprocess

import (
	"context"
	"os/exec"
	"testing"
)

func TestStartRefusesACommandItsContextWouldStopBehindItsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child, err := Start(ctx, exec.CommandContext(ctx, "go", "version"), Options{})

	if err == nil {
		_ = child.Wait()
		t.Fatal("Start ran a command made with exec.CommandContext, whose cancel kills only the direct child after WaitDelay while Start stops the whole group")
	}
}
