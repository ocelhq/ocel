package childprocess

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing"
)

func TestStartRefusesACommandItsContextWouldStopBehindItsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child, err := Start(ctx, tracing.CommandContext(ctx, "go", "version"), Options{})

	if err == nil {
		_ = child.Wait()
		t.Fatal("Start ran a command made with tracing.CommandContext, whose cancel kills only the direct child after WaitDelay while Start stops the whole group")
	}
}
