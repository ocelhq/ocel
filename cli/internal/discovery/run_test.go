package discovery

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/pkg/constants"
)

func jsFixture(t *testing.T, source string) (string, Prepared) {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, constants.DefaultDiscoveryDirName, "main.ts"), source)

	return root, prepare(t, root)
}

func prepare(t *testing.T, root string) Prepared {
	t.Helper()
	roots, err := Roots(root, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	prepared, err := Prepare(root, roots)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	return prepared
}

func okServer(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestRunReportsTheActualErrorFromAThrowingDeclareFile(t *testing.T) {
	root, prepared := jsFixture(t, `throw new Error("resource declared twice: db");`)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), root, prepared, okServer(t), &stdout, &stderr)
	if err == nil {
		t.Fatal("Run succeeded on a throwing declare file, want error")
	}
	if !strings.Contains(err.Error(), "resource declared twice: db") {
		t.Errorf("error = %q, want it to carry the actual error the declare file threw", err)
	}
}

func TestRunForwardsOutputThatIsNotPartOfTheProtocol(t *testing.T) {
	root, prepared := jsFixture(t, `console.log("hello from user code");
declare global { var __ocelRegister: Promise<unknown>[]; }
globalThis.__ocelRegister ??= [];
export {};
`)

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), root, prepared, okServer(t), &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello from user code") {
		t.Errorf("stdout = %q, want the user's own console.log forwarded", stdout.String())
	}
	if strings.Contains(stdout.String(), "@@OCEL_V1@@") {
		t.Errorf("stdout = %q, want protocol records consumed rather than shown to the user", stdout.String())
	}
}

func TestRunWritesToStdoutAndStderrConcurrentlyWithoutRacingWhenTheyAreTheSameWriter(t *testing.T) {
	root, prepared := jsFixture(t, `declare global { var __ocelRegister: Promise<unknown>[]; }
globalThis.__ocelRegister ??= [];
for (let i = 0; i < 4000; i++) {
  console.log("out", i);
  console.error("err", i);
}
export {};
`)

	var shared bytes.Buffer
	if err := Run(context.Background(), root, prepared, okServer(t), &shared, &shared); err != nil {
		t.Fatalf("Run: %v; output=%s", err, shared.String())
	}
}

func TestRunProducesADiscoverySpan(t *testing.T) {
	root, prepared := jsFixture(t, `declare global { var __ocelRegister: Promise<unknown>[]; }
globalThis.__ocelRegister ??= [];
export {};
`)

	dir := t.TempDir()
	ctx, run, err := runtrace.Start(context.Background(), dir, "ocel dev")
	if err != nil {
		t.Fatalf("runtrace.Start: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(ctx, root, prepared, okServer(t), &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if err := run.Close(); err != nil {
		t.Fatalf("run.Close: %v", err)
	}

	trace := readTraceFile(t, run)
	if !strings.Contains(trace, `"name": "discovery"`) {
		t.Errorf("trace = %s, want a span named discovery", trace)
	}
	if strings.Contains(trace, `"STATUS_CODE_ERROR"`) {
		t.Errorf("trace = %s, want the discovery span to succeed", trace)
	}
}

func readTraceFile(t *testing.T, run *runtrace.Run) string {
	t.Helper()
	path := strings.TrimSuffix(run.LogPath(), ".ndjson") + ".otlp.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	return string(raw)
}

func TestRunDeclaresAgainstTheCollectorAndSyncsOnceAfterTheChildExits(t *testing.T) {
	var mu sync.Mutex
	var order []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	root, prepared := jsFixture(t, `declare global { var __ocelRegister: Promise<unknown>[]; }
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(
  fetch(new URL("/app.resources.v1.ResourceService/Declare", process.env.`+constants.DevServerEnvName+`), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ resource: { type: "RESOURCE_TYPE_POSTGRES", name: "main" }, postgres: { version: "17" }, source: "`+constants.DefaultDiscoveryDirName+`/main.ts:1" }),
  }),
);
export {};
`)

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), root, prepared, server.URL, &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 {
		t.Fatalf("requests = %v, want a declare and a sync", order)
	}
	if !strings.HasSuffix(order[0], "/Declare") {
		t.Errorf("requests = %v, want the declare first", order)
	}
	if order[1] != "/sync" {
		t.Errorf("requests = %v, want sync posted once, last", order)
	}
}

func TestRunRefusesARootThisBuildCannotDiscover(t *testing.T) {
	root := t.TempDir()
	unknown := Root{Dir: filepath.Join(root, "unknown"), Language: Language("ruby")}

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), root, Prepared{Roots: []Root{unknown}}, okServer(t), &stdout, &stderr)
	if err == nil {
		t.Fatal("Run succeeded on a root written in a language ocel has no launcher for, want an error")
	}
	want := "is a ruby folder, and this build of ocel discovers only go, js, python and rust folders"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want it to contain %q", err, want)
	}
	if !strings.Contains(err.Error(), unknown.Dir) {
		t.Errorf("err = %v, want it to name the root", err)
	}
}

func TestRunBuildsNoBundleAndRunsNoNodeWithoutAJSRoot(t *testing.T) {
	root := t.TempDir()

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), root, prepare(t, root), okServer(t), &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stderr=%s", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(root, constants.ProjectStateDirName, "entry.mjs")); !os.IsNotExist(err) {
		t.Errorf("stat entry.mjs = %v, want a project with no js root to bundle nothing and run no node", err)
	}
}

func TestRunBundlesNothingForARootItCannotDiscover(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "unknown", "declarations.rb"), "")

	roots, err := Roots(root, nil)
	if err != nil {
		t.Fatalf("Roots: %v", err)
	}
	prepared, err := Prepare(root, append(roots, Root{Dir: filepath.Join(root, "unknown"), Language: "ruby"}))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), root, prepared, okServer(t), &stdout, &stderr); err == nil {
		t.Fatal("Run succeeded on a root of a language ocel does not discover, want an error")
	}
	if _, err := os.Stat(filepath.Join(root, constants.ProjectStateDirName, "entry.mjs")); !os.IsNotExist(err) {
		t.Errorf("stat entry.mjs = %v, want a project with no js root to bundle nothing", err)
	}
}
