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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/workdir"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/node"
	"github.com/ocelhq/ocel/pkg/processenv"
)

const greetEnvelope = `{"v":1,"topic":"greet","consumer":"greet","execution":"01JZ8X6Q3V5W7Y9A1C3E5G7J9K-greet",` +
	`"message":{"id":"01JZ8X6Q3V5W7Y9A1C3E5G7J9K"},"attempt":{"number":1,"of":3},"payload":{"name":"ada"}}`

const auditEnvelope = `{"v":1,"topic":"orders","consumer":"audit","execution":"01JZ8X6Q3V5W7Y9A1C3E5G7J9M-audit",` +
	`"message":{"id":"01JZ8X6Q3V5W7Y9A1C3E5G7J9M"},"attempt":{"number":1,"of":3},"payload":{"name":"grace"}}`

const lingerEnvelope = `{"v":1,"topic":"linger","consumer":"linger","execution":"01JZ8X6Q3V5W7Y9A1C3E5G7J9N-linger",` +
	`"message":{"id":"01JZ8X6Q3V5W7Y9A1C3E5G7J9N"},"attempt":{"number":1,"of":3},"payload":{"name":"ada"}}`

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
	child, err := childprocess.Start(context.Background(), cmd, childprocess.Options{})
	if err != nil {
		t.Fatalf("start the worker: %v", err)
	}
	t.Cleanup(child.Stop)

	address := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case waited := <-child.Exited():
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

func wantAudited(t *testing.T, url string) {
	t.Helper()
	if answer := deliverTo(t, url, auditEnvelope); answer.status != http.StatusOK {
		t.Fatalf("consumer answer = %d %s, want 200", answer.status, answer.body)
	}
	answer := deliverTo(t, url, greetEnvelope)
	var output struct {
		Audited string `json:"audited"`
	}
	if err := json.Unmarshal([]byte(answer.body), &output); err != nil {
		t.Fatalf("answer body %q is not the run's JSON output: %v", answer.body, err)
	}
	if want := "consumer:audit grace"; output.Audited != want {
		t.Errorf("audited = %q, want %q: the consumer ran on the message, inside the worker's middleware", output.Audited, want)
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

	run := workdir.NewRun()
	t.Cleanup(func() { _ = run.Remove() })
	cmd, err := WorkerCommand(context.Background(), configDir, roots, roots[0], run)
	if err != nil {
		t.Fatalf("WorkerCommand: %v", err)
	}
	return startWorker(t, cmd, "worker")
}

func TestAWorkerIsServedFromTheRootItsTasksAndConsumersAreDeclaredIn(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	node := Root{Dir: filepath.Join(configDir, "web"), Language: language.JS}
	python := Root{Dir: filepath.Join(configDir, "jobs", "py"), Language: language.Python}
	crate := Root{Dir: configDir, Language: language.Rust}
	roots := []Root{node, python, crate}

	for _, tc := range []struct {
		sources []string
		want    Root
	}{
		{[]string{filepath.Join(configDir, "web", "tasks", "greet.ts") + ":3"}, node},
		{[]string{"jobs/py/__init__.py:12", filepath.Join(configDir, "jobs", "py", "audit.py") + ":4"}, python},
		{[]string{"src/main.rs:30"}, crate},
	} {
		got, err := WorkerRoot(configDir, roots, "worker", tc.sources)
		if err != nil || got != tc.want {
			t.Errorf("WorkerRoot(%q) = %+v, %v, want %+v", tc.sources, got, err, tc.want)
		}
	}

	_, err := WorkerRoot(configDir, roots, "media", []string{"web/greet.ts:3", "jobs/py/audit.py:4"})
	if err == nil || !strings.Contains(err.Error(), `worker "media"`) || !strings.Contains(err.Error(), "web/greet.ts:3") || !strings.Contains(err.Error(), "jobs/py/audit.py:4") {
		t.Errorf("WorkerRoot across a JS and a Python root = %v, want a refusal naming the worker and both declarations", err)
	}
}

func TestAGoWorkerRunsTheRootsOfItsOwnModuleAndNoOther(t *testing.T) {
	configDir := t.TempDir()
	for _, module := range []string{"a", "b"} {
		write(t, filepath.Join(configDir, module, "go.mod"), "module example.com/"+module+"\n\ngo 1.27.0\n")
		write(t, filepath.Join(configDir, module, "declarations", "declarations.go"), "package declarations\n")
		write(t, filepath.Join(configDir, module, "jobs", "jobs.go"), "package jobs\n")
	}
	root := func(module, dir string) Root {
		return Root{Dir: filepath.Join(configDir, module, dir), Language: language.Go}
	}
	roots := []Root{root("a", "declarations"), root("a", "jobs"), root("b", "declarations"), root("b", "jobs"), {Dir: filepath.Join(configDir, "web"), Language: language.JS}}

	got, err := WorkerRoots(configDir, roots, root("a", "jobs"))
	if err != nil {
		t.Fatalf("WorkerRoots: %v", err)
	}
	if want := []Root{root("a", "declarations"), root("a", "jobs")}; !slices.Equal(got, want) {
		t.Errorf("WorkerRoots = %v, want %v: a go worker imports every root of its module, and only those", got, want)
	}
}

func TestAWorkerDeclaredOutsideEveryRootIsNamedByItsPathInTheProjectEvenUnderAFolderStartingWithTwoDots(t *testing.T) {
	t.Parallel()
	configDir := t.TempDir()
	roots := []Root{{Dir: filepath.Join(configDir, DefaultRootDirName), Language: language.JS}, {Dir: filepath.Join(configDir, "jobs"), Language: language.Python}}

	_, err := WorkerRoot(configDir, roots, "worker", []string{filepath.Join(configDir, "..jobs", "greet.ts") + ":3"})
	if err == nil || !strings.Contains(err.Error(), "serves ..jobs/greet.ts:3,") {
		t.Errorf("WorkerRoot = %v, want a refusal naming ..jobs/greet.ts:3, the declaration's path in the project", err)
	}
}

func TestTheGeneratedNodeWorkerServesATaskAndAConsumerWithItsWorkersOnStartAndMiddleware(t *testing.T) {
	url := servedFixture(t, "node")

	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantAudited(t, url)
}

func TestTheNodeWorkerBundleServesFromADirectoryWithNoNodeModulesBesideIt(t *testing.T) {
	configDir := repoFixture(t, filepath.Join("worker", "node"))
	roots, err := RootsOf(&project.Project{Dir: configDir, Apps: []project.App{{Name: "web", Path: "."}}})
	if err != nil {
		t.Fatalf("RootsOf: %v", err)
	}
	image := t.TempDir()
	entry := filepath.Join(image, "worker.mjs")
	if err := BundleNodeWorker(configDir, roots, entry); err != nil {
		t.Fatalf("BundleNodeWorker: %v", err)
	}
	cmd := exec.Command("node", entry)
	cmd.Dir = image
	cmd.Env = os.Environ()
	url := startWorker(t, cmd, "worker")

	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantAudited(t, url)
}

func TestTheNodeWorkerADeployShipsCarriesNoTracingWhileTheLocalWorkerExportsSpans(t *testing.T) {
	configDir := repoFixture(t, filepath.Join("worker", "node"))
	roots, err := RootsOf(&project.Project{Dir: configDir, Apps: []project.App{{Name: "web", Path: "."}}})
	if err != nil {
		t.Fatalf("RootsOf: %v", err)
	}
	const exporter = "startExport"

	deployed := filepath.Join(t.TempDir(), "worker.mjs")
	if err := BundleNodeWorker(configDir, roots, deployed); err != nil {
		t.Fatalf("BundleNodeWorker: %v", err)
	}
	image, err := os.ReadFile(deployed)
	if err != nil {
		t.Fatal(err)
	}
	for _, linked := range []string{exporter, "@opentelemetry"} {
		if bytes.Contains(image, []byte(linked)) {
			t.Errorf("the worker bundle an image ships contains %q: deployed workload code carries no tracing", linked)
		}
	}
	source, _, err := RenderNodeWorkerEntry(configDir, roots)
	if err != nil {
		t.Fatalf("RenderNodeWorkerEntry: %v", err)
	}
	installed, err := node.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(source, strconv.Quote(installed.WorkerServerPath())) {
		t.Errorf("the worker entry a function ships imports\n%s\nwant the untraced server %s", source, installed.WorkerServerPath())
	}

	run := workdir.NewRun()
	t.Cleanup(func() { _ = run.Remove() })
	cmd, err := WorkerCommand(context.Background(), configDir, roots, roots[0], run)
	if err != nil {
		t.Fatalf("WorkerCommand: %v", err)
	}
	local, err := os.ReadFile(cmd.Args[len(cmd.Args)-1])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(local, []byte(exporter)) {
		t.Errorf("the local worker bundle has no %s: a worker run by ocel exports its spans to the run's trace", exporter)
	}
}

func TestTheGeneratedGoWorkerServesATaskAndAConsumerWithItsWorkersOnStartAndMiddleware(t *testing.T) {
	url := servedFixture(t, "go")

	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantAudited(t, url)
}

func TestARustBinaryInTheWorkerRoleServesATaskAndAConsumerWithItsWorkersOnStartAndMiddlewareAndNeverEntersMain(t *testing.T) {
	needsCargo(t)
	url := servedFixture(t, "rust")

	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantAudited(t, url)
}

func TestTheRustDevWorkerRefusesABinaryWithoutOcelMainInsteadOfStartingTheApp(t *testing.T) {
	needsCargo(t)
	configDir := rustFixture(t, rustBinManifest)
	write(t, filepath.Join(configDir, "src", "main.rs"), servesForever)
	crate := Root{Dir: configDir, Language: language.Rust}

	run := workdir.NewRun()
	t.Cleanup(func() { _ = run.Remove() })
	_, err := WorkerCommand(context.Background(), configDir, []Root{crate}, crate, run)
	if err == nil || !strings.Contains(err.Error(), "#[ocel::main]") || !strings.Contains(err.Error(), `"web"`) {
		t.Fatalf("WorkerCommand = %v, want a refusal asking the binary web for #[ocel::main]", err)
	}
}

func TestTheGeneratedPythonWorkerServesATaskAndAConsumerWithItsWorkersOnStartAndMiddleware(t *testing.T) {
	t.Setenv("PATH", pythonSDKEnvironment(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
	url := servedFixture(t, "python")

	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantGreeted(t, deliverTo(t, url, greetEnvelope), 1)
	wantAudited(t, url)
}

func TestTheRenderedPythonWorkerEntryImportsFromTheDirectoryItRunsIn(t *testing.T) {
	t.Setenv("PATH", pythonSDKEnvironment(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
	configDir := repoFixture(t, filepath.Join("worker", "python"))
	roots, err := RootsOf(&project.Project{Dir: configDir, Apps: []project.App{{Name: "web", Path: "."}}})
	if err != nil {
		t.Fatalf("RootsOf: %v", err)
	}
	packages, err := ListPythonPackages(configDir, roots, configDir)
	if err != nil {
		t.Fatalf("ListPythonPackages: %v", err)
	}
	entry := RenderPythonWorkerEntry(packages)
	if strings.Contains(entry, configDir) {
		t.Errorf("the entry names the build host's %s, which is nowhere the artifact runs", configDir)
	}

	artifact := t.TempDir()
	if err := os.CopyFS(artifact, os.DirFS(configDir)); err != nil {
		t.Fatalf("copy the app into the artifact: %v", err)
	}
	entryFile := filepath.Join(t.TempDir(), "worker.py")
	if err := os.WriteFile(entryFile, []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python3", entryFile)
	cmd.Dir = artifact
	cmd.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")

	wantGreeted(t, deliverTo(t, startWorker(t, cmd, "worker"), greetEnvelope), 1)
}

func TestAPythonWorkerInDevImportsTheFoldersItsProjectSharesAndNoOtherApps(t *testing.T) {
	t.Parallel()
	configDir, roots := twoPythonApps(t)

	run := workdir.NewRun()
	t.Cleanup(func() { _ = run.Remove() })
	cmd, err := WorkerCommand(context.Background(), configDir, roots, roots[0], run)
	if err != nil {
		t.Fatalf("WorkerCommand: %v", err)
	}
	entry, err := os.ReadFile(filepath.Join(cmd.Dir, filepath.FromSlash(cmd.Args[len(cmd.Args)-1])))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`import_module("jobs")`, `import_module("` + DefaultRootDirName + `")`} {
		if !strings.Contains(string(entry), want) {
			t.Errorf("the dev worker entry lacks %s:\n%s", want, entry)
		}
	}
	if strings.Contains(string(entry), `"tasks"`) {
		t.Errorf("the dev worker entry imports b's tasks:\n%s", entry)
	}
	var pythonPath []string
	for _, entry := range cmd.Env {
		if value, found := strings.CutPrefix(entry, "PYTHONPATH="); found {
			pythonPath = filepath.SplitList(value)
		}
	}
	if !slices.Contains(pythonPath, configDir) {
		t.Errorf("the dev worker's PYTHONPATH = %q, want it to hold %s, where %s is imported from", pythonPath, configDir, DefaultRootDirName)
	}
}

func TestTheGeneratedPythonWorkerCancelsARunWhoseCallerHungUp(t *testing.T) {
	t.Setenv("PATH", pythonSDKEnvironment(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
	url := servedFixture(t, "python")

	hangUp, hungUp := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer hungUp()
	req, err := http.NewRequestWithContext(hangUp, http.MethodPost, url, strings.NewReader(lingerEnvelope))
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("the lingering run answered %d, want it still running when the caller hung up", resp.StatusCode)
	}

	var cancelled string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		var output struct {
			Cancelled string `json:"cancelled"`
		}
		if err := json.Unmarshal([]byte(deliverTo(t, url, greetEnvelope).body), &output); err != nil {
			t.Fatal(err)
		}
		if cancelled = output.Cancelled; cancelled != "" {
			break
		}
	}
	if want := "linger ada"; cancelled != want {
		t.Errorf("cancelled = %q, want %q: a run whose caller hung up is cancelled and its on_cancel hook run", cancelled, want)
	}
}

func TestTheWorkerProgramsReadTheWorkerTheCLINamesAndImportWhatTheyAreGiven(t *testing.T) {
	for name, program := range map[string]string{"worker_main.go.tmpl": goWorkerMain, "worker.py": pythonWorkerServer} {
		if !strings.Contains(program, `"`+processenv.WorkerEnvVar+`"`) {
			t.Errorf("%s does not read %s, the variable the CLI names the worker in", name, processenv.WorkerEnvVar)
		}
	}
	rendered := RenderPythonWorkerEntry([]PythonPackage{{Name: "shop"}})
	if !strings.Contains(rendered, `import_module("shop")`) || strings.Contains(rendered, importsMarker) {
		t.Errorf("rendered Python worker =\n%s\nwant the package imported in place of the marker", rendered)
	}
}
