package discovery

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

const registerSideEffect = `
declare global {
  var __ocelRegister: Promise<unknown>[];
}
globalThis.__ocelRegister ??= [];
globalThis.__ocelRegister.push(Promise.resolve());
export {};
`

func TestBundle_RunsImportsAndPostsSyncExactlyOnce(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ocel", "main.ts"), registerSideEffect)

	files, err := Discover(root, []string{"ocel"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	entry, err := Bundle(root, files)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}

	var syncCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync" && r.Method == http.MethodPost {
			atomic.AddInt32(&syncCalls, 1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cmd := exec.Command("node", entry)
	cmd.Env = append(cmd.Environ(), "OCEL_DEV_SERVER="+server.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run bundled entry: %v\n%s", err, out)
	}

	if got := atomic.LoadInt32(&syncCalls); got != 1 {
		t.Fatalf("sync calls = %d, want 1", got)
	}
}

func TestBundle_FailedSyncFailsTheProcess(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ocel", "main.ts"), registerSideEffect)

	files, err := Discover(root, []string{"ocel"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	entry, err := Bundle(root, files)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "provisioning failed", http.StatusInternalServerError)
	}))
	defer server.Close()

	cmd := exec.Command("node", entry)
	cmd.Env = append(cmd.Environ(), "OCEL_DEV_SERVER="+server.URL)
	if err := cmd.Run(); err == nil {
		t.Fatal("expected non-zero exit when /sync fails, got nil error")
	}
}

// Declaration files routinely pull in CJS dependencies (e.g. node-postgres
// via the SDK's postgres()) whose require("events")-style builtin loads
// esbuild leaves as runtime require calls. In ESM output those crash with
// "Dynamic require of ... is not supported" unless the bundle provides a
// createRequire-backed shim.
func TestBundle_RunsCJSDepsThatRequireNodeBuiltins(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "node_modules", "cjsdep", "package.json"),
		`{"name": "cjsdep", "version": "1.0.0", "main": "index.js"}`)
	write(t, filepath.Join(root, "node_modules", "cjsdep", "index.js"),
		`const { EventEmitter } = require("events");
module.exports = { emitter: new EventEmitter() };
`)
	write(t, filepath.Join(root, "ocel", "main.ts"), `import "cjsdep";`+registerSideEffect)

	files, err := Discover(root, []string{"ocel"})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	entry, err := Bundle(root, files)
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cmd := exec.Command("node", entry)
	cmd.Env = append(cmd.Environ(), "OCEL_DEV_SERVER="+server.URL)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run bundled entry with CJS builtin-requiring dep: %v\n%s", err, out)
	}
}
