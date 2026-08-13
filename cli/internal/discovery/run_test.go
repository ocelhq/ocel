package discovery

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/obs"
)

func bundleFixture(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "ocel", "main.ts"), source)

	files, err := Discover(root, []string{"ocel"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	entry, err := Bundle(root, files)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	return entry
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
	entry := bundleFixture(t, `throw new Error("resource declared twice: db");`)

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), entry, okServer(t), &stdout, &stderr)
	if err == nil {
		t.Fatal("Run succeeded on a throwing declare file, want error")
	}
	if !strings.Contains(err.Error(), "resource declared twice: db") {
		t.Errorf("error = %q, want it to carry the actual error the declare file threw", err)
	}
}

func TestRunForwardsOutputThatIsNotPartOfTheProtocol(t *testing.T) {
	entry := bundleFixture(t, `console.log("hello from user code");
declare global { var __ocelRegister: Promise<unknown>[]; }
globalThis.__ocelRegister ??= [];
export {};
`)

	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), entry, okServer(t), &stdout, &stderr); err != nil {
		t.Fatalf("Run: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello from user code") {
		t.Errorf("stdout = %q, want the user's own console.log forwarded", stdout.String())
	}
	if strings.Contains(stdout.String(), "@@OCEL_V1@@") {
		t.Errorf("stdout = %q, want protocol records consumed rather than shown to the user", stdout.String())
	}
}

func TestRunProducesADiscoverySpan(t *testing.T) {
	entry := bundleFixture(t, `declare global { var __ocelRegister: Promise<unknown>[]; }
globalThis.__ocelRegister ??= [];
export {};
`)

	dir := t.TempDir()
	ctx, run, err := obs.Start(context.Background(), dir, "ocel dev")
	if err != nil {
		t.Fatalf("obs.Start: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := Run(ctx, entry, okServer(t), &stdout, &stderr); err != nil {
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

func readTraceFile(t *testing.T, run *obs.Run) string {
	t.Helper()
	path := strings.TrimSuffix(run.LogPath(), ".ndjson") + ".otlp.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	return string(raw)
}
