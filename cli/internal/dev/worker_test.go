package dev

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
)

func workerOptions(t *testing.T, stdout, stderr io.Writer) Options {
	t.Helper()
	bus := run.NewBus(time.Now)
	bus.Attach(terminal.NewTranscript(stderr, terminal.Resolve(terminal.Conditions{})))
	t.Cleanup(func() { _ = bus.Close() })
	_, begun, err := bus.Begin(context.Background(), "ocel dev", "")
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	return Options{Project: &project.Project{Dir: t.TempDir()}, Stdout: stdout, Stderr: stderr, Run: begun}
}

func TestAWorkerListensOnLoopbackWhateverHostTheShellOrTheAppExports(t *testing.T) {
	t.Setenv("HOST", "0.0.0.0")
	var stdout, stderr syncBuffer
	workers := newWorkerProcesses(workerOptions(t, &stdout, &stderr), nil)

	cmd := exec.CommandContext(context.Background(), "sh", "-c", `printf %s "$HOST"`)
	cmd.Env = os.Environ()
	process, err := workers.start(context.Background(), cmd, "worker", 4000, map[string]string{"HOST": "0.0.0.0"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	<-process.exited
	if got := stdout.String(); got != "127.0.0.1" {
		t.Errorf("the worker saw HOST=%q, want 127.0.0.1, the address ocel dev dials it on", got)
	}
}
