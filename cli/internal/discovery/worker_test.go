package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/processenv"
)

const greetEnvelope = `{"v":1,"topic":"greet","consumer":"greet","execution":"01JZ8X6Q3V5W7Y9A1C3E5G7J9K-greet",` +
	`"message":{"id":"01JZ8X6Q3V5W7Y9A1C3E5G7J9K"},"attempt":{"number":1,"of":3},"payload":{"name":"ada"}}`

type workerAnswer struct {
	status int
	body   string
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find a free port: %v", err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func startWorker(t *testing.T, cmd *exec.Cmd, worker string) string {
	t.Helper()
	port := freeLoopbackPort(t)
	cmd.Env = append(cmd.Env, processenv.WorkerEnvVar+"="+worker, "PORT="+strconv.Itoa(port))
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	childprocess.SetOwnGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the worker: %v", err)
	}
	exited := make(chan struct{})
	var waited error
	go func() {
		waited = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		_ = childprocess.KillGroup(cmd)
		<-exited
	})

	address := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			t.Fatalf("the worker exited before it listened: %v\n%s", waited, output.String())
		default:
		}
		if conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond); err == nil {
			_ = conn.Close()
			return "http://" + address
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the worker never listened on %s:\n%s", address, output.String())
	return ""
}

func deliverTo(t *testing.T, url, envelope string) workerAnswer {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(envelope))
	if err != nil {
		t.Fatalf("POST the envelope: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	return workerAnswer{status: resp.StatusCode, body: string(body)}
}

func wantGreeted(t *testing.T, answer workerAnswer, wantStarts int) {
	t.Helper()
	if answer.status != http.StatusOK {
		t.Fatalf("answer = %d %s, want 200 with the run's output", answer.status, answer.body)
	}
	var output struct {
		Greeting  string `json:"greeting"`
		Starts    int    `json:"starts"`
		WrappedBy string `json:"wrappedBy"`
	}
	if err := json.Unmarshal([]byte(answer.body), &output); err != nil {
		t.Fatalf("answer body %q is not the run's JSON output: %v", answer.body, err)
	}
	want := fmt.Sprintf("{hello ada %d task:greet}", wantStarts)
	if got := fmt.Sprintf("{%s %d %s}", output.Greeting, output.Starts, output.WrappedBy); got != want {
		t.Errorf("output = %s, want %s: the run's greeting, onStart run once, and the worker's middleware around the run", got, want)
	}
}

func servedFixture(t *testing.T, fixture string) string {
	t.Helper()
	configDir := repoFixture(t, filepath.Join("worker", fixture))
	roots, err := RootsOf(&project.Project{Dir: configDir, Apps: []project.App{{Name: "web", Path: "."}}})
	if err != nil {
		t.Fatalf("RootsOf: %v", err)
	}
	if len(roots) != 1 {
		t.Fatalf("roots = %+v, want the fixture's one discovery root", roots)
	}

	cmd, err := WorkerCommand(context.Background(), configDir, roots, roots[0])
	if err != nil {
		t.Fatalf("WorkerCommand: %v", err)
	}
	return startWorker(t, cmd, "worker")
}

func TestTheGeneratedNodeWorkerServesATaskWithItsWorkersOnStartAndMiddleware(t *testing.T) {
	url := servedFixture(t, "node")

	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
}

func TestTheGeneratedGoWorkerServesATaskWithItsWorkersOnStartAndMiddleware(t *testing.T) {
	url := servedFixture(t, "go")

	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
}

func TestARustBinaryInTheWorkerRoleServesATaskWithItsWorkersOnStartAndMiddlewareAndNeverEntersMain(t *testing.T) {
	needsCargo(t)
	url := servedFixture(t, "rust")

	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
}

func TestTheGeneratedPythonWorkerServesATaskWithItsWorkersOnStartAndMiddleware(t *testing.T) {
	t.Setenv("PATH", pythonSDKEnvironment(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
	url := servedFixture(t, "python")

	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
}
