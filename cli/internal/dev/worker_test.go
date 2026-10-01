package dev

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/fixturetest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

const liveDockerEnv = "OCEL_LIVE_DOCKER"

const greetTask = `import { task } from "ocel/task";

export const greet = task("greet", {
  run: (payload: { name: string }) => ({ greeting: "hello " + payload.name }),
});
`

const triggerGreet = `import { writeFileSync } from "node:fs";
import { runs } from "ocel/task";
import { greet } from "./%[1]s/index.ts";

const handle = await greet.trigger({ name: "ada" });
writeFileSync(new URL("run.id", import.meta.url), handle.id);
for (;;) {
  const run = await runs.retrieve(handle.id);
  if (run.status === "COMPLETED") {
    writeFileSync(new URL("run.output", import.meta.url), JSON.stringify(run.output));
    break;
  }
  await new Promise((resolve) => setTimeout(resolve, 100));
}
`

const retrieveRun = `import { readFileSync } from "node:fs";
import { runs } from "ocel/task";

const run = await runs.retrieve(readFileSync(new URL("run.id", import.meta.url), "utf8"));
process.exit(run.status === "COMPLETED" ? 0 : 3);
`

const editGreet = `import { writeFileSync } from "node:fs";
import { runs } from "ocel/task";
import { greet } from "./%[1]s/index.ts";

async function greeting() {
  const handle = await greet.trigger({ name: "ada" });
  for (;;) {
    const run = await runs.retrieve(handle.id);
    if (run.status === "COMPLETED") return run.output.greeting;
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

if ((await greeting()) !== "hello ada") process.exit(3);
writeFileSync(new URL("%[1]s/index.ts", import.meta.url), %[2]q);
const deadline = Date.now() + 120_000;
while (Date.now() < deadline) {
  if ((await greeting()) === "hi ada") process.exit(0);
}
process.exit(4);
`

func liveProject(t *testing.T, declarations string) string {
	t.Helper()
	if os.Getenv(liveDockerEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveDockerEnv)
	}
	root := t.TempDir()
	t.Cleanup(func() {
		releaseLeader(root)
		ctx := context.Background()
		engine, err := docker.Open(ctx)
		if err != nil {
			return
		}
		_ = engine.Wipe(ctx, docker.ProjectLabels(devresources.ProjectName(root)))
		_ = engine.Close()
	})
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "worker-dev", "provider": "aws", "apps": [{"name": "web", "path": "."}]}`)
	clitest.WriteFile(t, filepath.Join(root, "package.json"), `{"name": "worker-dev", "type": "module"}`)
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(root), "index.ts"), declarations)
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(fixturetest.RepoDir(t), "packages", "ocel"), filepath.Join(root, "node_modules", "ocel")); err != nil {
		t.Fatal(err)
	}
	return root
}

const auditConsumer = `import { writeFileSync } from "node:fs";
import { topic } from "ocel/topic";

export const orders = topic<{ name: string }>("orders");

export const audit = orders.consumer("audit", (payload) => {
  writeFileSync(%q, payload.name);
});
`

const sendOrder = `import { existsSync, readFileSync } from "node:fs";
import { orders } from "./%[1]s/index.ts";

await orders.send({ name: "ada" });
const deadline = Date.now() + 120_000;
while (Date.now() < deadline) {
  if (existsSync(%[2]q) && readFileSync(%[2]q, "utf8") === "ada") process.exit(0);
  await new Promise((resolve) => setTimeout(resolve, 100));
}
process.exit(4);
`

func taskProject(t *testing.T) string {
	t.Helper()
	root := liveProject(t, greetTask)
	clitest.WriteFile(t, filepath.Join(root, "trigger.ts"), fmt.Sprintf(triggerGreet, discovery.DefaultRootDirName))
	clitest.WriteFile(t, filepath.Join(root, "retrieve.ts"), retrieveRun)
	return root
}

func TestDockerOcelDevRunsATriggeredTaskOnItsWorkerPrintsTheRunAndKeepsItAcrossRestarts(t *testing.T) {
	root := taskProject(t)
	deps := devDeps()
	deps.OpenDocker = docker.Open

	var stdout, stderr syncBuffer
	if err := runDev(context.Background(), deps, false, root, []string{"node", filepath.Join(root, "trigger.ts")}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDev err = %v; stderr=%s", err, stderr.String())
	}
	id, err := os.ReadFile(filepath.Join(root, "run.id"))
	if err != nil {
		t.Fatalf("the app never triggered greet: %v; stderr=%s", err, stderr.String())
	}
	if output, err := os.ReadFile(filepath.Join(root, "run.output")); err != nil || string(output) != `{"greeting":"hello ada"}` {
		t.Fatalf("run output = %s, %v, want the worker's answer; stderr=%s", output, err, stderr.String())
	}
	if want := `task "greet" run ` + string(id) + ` completed`; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %s, want a line saying %q", stderr.String(), want)
	}

	var again syncBuffer
	if err := runDev(context.Background(), deps, false, root, []string{"node", filepath.Join(root, "retrieve.ts")}, &stdout, &again, strings.NewReader("")); err != nil {
		t.Fatalf("the run greet completed in the last ocel dev is not completed in the next: %v; stderr=%s", err, again.String())
	}
}

func TestDockerOcelDevDeliversAMessageSentToATopicToItsConsumerOnTheWorker(t *testing.T) {
	audited := filepath.Join(t.TempDir(), "audited")
	root := liveProject(t, fmt.Sprintf(auditConsumer, audited))
	clitest.WriteFile(t, filepath.Join(root, "send.ts"), fmt.Sprintf(sendOrder, discovery.DefaultRootDirName, audited))
	deps := devDeps()
	deps.OpenDocker = docker.Open

	var stdout, stderr syncBuffer
	if err := runDev(context.Background(), deps, false, root, []string{"node", filepath.Join(root, "send.ts")}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDev err = %v, want the consumer to record the message the app sent; stderr=%s", err, stderr.String())
	}
	if want := `consumer "audit" of topic "orders" execution `; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %s, want a line starting %q", stderr.String(), want)
	}
}

func TestDockerOcelDevRestartsTheWorkerWhenATaskChanges(t *testing.T) {
	root := taskProject(t)
	edited := strings.Replace(greetTask, `"hello "`, `"hi "`, 1)
	clitest.WriteFile(t, filepath.Join(root, "edit.ts"), fmt.Sprintf(editGreet, discovery.DefaultRootDirName, edited))
	deps := devDeps()
	deps.OpenDocker = docker.Open

	var stdout, stderr syncBuffer
	if err := runDev(context.Background(), deps, false, root, []string{"node", filepath.Join(root, "edit.ts")}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDev err = %v, want the edited task to answer once the worker restarted; stderr=%s", err, stderr.String())
	}
}

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

func TestDockerAWorkerStoppedForTheNextPassNeverReportsThatItExited(t *testing.T) {
	if os.Getenv(liveDockerEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveDockerEnv)
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not on PATH")
	}
	ctx := context.Background()
	dir := t.TempDir()
	cfg := &project.Project{Dir: dir, Slug: "worker-stop", Apps: []project.App{{Name: "web", Path: "."}}}
	name := devresources.ProjectName(dir)
	resources := devresources.New(name, devresources.Options{Open: docker.Open, StateDir: t.TempDir(), Project: cfg})
	t.Cleanup(func() {
		_ = resources.Close(ctx)
		engine, err := docker.Open(ctx)
		if err != nil {
			return
		}
		_ = engine.Wipe(ctx, docker.ProjectLabels(name))
		_ = engine.Close()
	})
	greet := declaration.Resource{Name: "greet", Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Task: &resourcesv1.TaskConfig{}, Source: filepath.Join(dir, "jobs", "index.ts") + ":3"}
	if _, err := resources.Resolve(ctx, []declaration.Resource{greet}); err != nil {
		t.Fatalf("Resolve = %v", err)
	}

	var stdout, stderr syncBuffer
	opts := workerOptions(t, &stdout, &stderr)
	opts.Project = cfg
	workers := newWorkerProcesses(opts, resources)
	slowToExit := exec.CommandContext(ctx, "sh", "-c", "setsid sh -c 'echo detached; sleep 3' & exec sleep 60")
	slowToExit.Env = os.Environ()
	process, err := workers.start(ctx, slowToExit, "worker", 4000, map[string]string{})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	workers.running = append(workers.running, process)
	for !strings.Contains(stdout.String(), "detached") {
		time.Sleep(10 * time.Millisecond)
	}

	workers.restart(ctx, map[string]string{})
	<-process.exited
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stderr.String(), "exited") {
			t.Fatalf("stderr = %s, want no report that a worker ocel dev stopped itself exited", stderr.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
