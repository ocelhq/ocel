package dev

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/devresources/queue"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/workdir"
	"github.com/ocelhq/ocel/pkg/tracing"
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

type fakeWorkerQueue struct {
	workers []queue.Worker
}

func (q *fakeWorkerQueue) Workers() []queue.Worker { return q.workers }

func (q *fakeWorkerQueue) DeliverTo(context.Context, map[string]string) error { return nil }

func TestARediscoveryRestartsOnlyTheWorkersWhoseRootsChanged(t *testing.T) {
	dir := t.TempDir()
	var workers []queue.Worker
	for _, module := range []string{"a", "b"} {
		source := filepath.Join(dir, module, "jobs.go")
		writeTestFile(t, filepath.Join(dir, module, "go.mod"), "module example.com/"+module+"\n\ngo 1.27.0\n")
		writeTestFile(t, source, "package "+module+"\n")
		workers = append(workers, queue.Worker{Name: module, Sources: []string{source + ":1"}})
	}
	var stdout, stderr syncBuffer
	opts := workerOptions(t, &stdout, &stderr)
	opts.Project = &project.Project{Dir: dir, DiscoveryPaths: []string{"a", "b"}}
	processes := newWorkerProcesses(opts, &fakeWorkerQueue{workers: workers})
	processes.command = func(ctx context.Context, _ string, _ []discovery.Root, _ discovery.Root, _ *workdir.Run) (*tracing.Cmd, error) {
		return tracing.Command(context.Background(), "sleep", "60"), nil
	}
	ctx := context.Background()
	t.Cleanup(func() { processes.close(ctx) })
	env := map[string]string{"GREETING": "hello"}

	processes.restart(ctx, env, nil)
	a, b := processes.running["a"], processes.running["b"]
	if a == nil || b == nil {
		t.Fatalf("running = %v, want workers a and b started; stderr=%s", processes.running, stderr.String())
	}

	processes.restart(ctx, env, []string{filepath.Join(dir, "a", "jobs.go")})
	if processes.running["a"] == a {
		t.Error("worker a still runs the process it ran before its root changed, want it restarted")
	}
	if processes.running["b"] != b {
		t.Error("worker b was restarted for a change outside its roots, want it left running")
	}

	a = processes.running["a"]
	processes.restart(ctx, map[string]string{"GREETING": "hi"}, nil)
	if processes.running["a"] == a || processes.running["b"] == b {
		t.Error("a worker kept running after the values every worker reads changed, want them all restarted")
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAWorkerListensOnLoopbackWhateverHostTheShellOrTheAppExports(t *testing.T) {
	t.Setenv("HOST", "0.0.0.0")
	var stdout, stderr syncBuffer
	workers := newWorkerProcesses(workerOptions(t, &stdout, &stderr), nil)

	cmd := tracing.Command(context.Background(), "sh", "-c", `printf %s "$HOST"`)
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
