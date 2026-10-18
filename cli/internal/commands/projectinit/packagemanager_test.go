package projectinit

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess/childprocesstest"
)

func TestAnInterruptedPackageManagerStopsWithEveryChildHoldingItsOutput(t *testing.T) {
	root := t.TempDir()
	command, started, pidPath := childprocesstest.WorkerTree(t, root, "postinstall")
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan error, 1)
	go func() { ran <- RunPackageManager(ctx, root, command, &strings.Builder{}) }()
	childprocesstest.WaitForFile(t, started)

	cancel()

	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("the package manager still runs 10s after an interrupt, held open by a child that survived it")
	}
	childprocesstest.WaitDead(t, pidPath)
}
