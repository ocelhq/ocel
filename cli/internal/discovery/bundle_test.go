package discovery

import (
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestBundle(t *testing.T) {
	t.Run("runs its imports and leaves sync to the caller", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "infra", "main.ts"), registerSideEffect+`
console.log("declared");
`)

		files, err := Discover(root, []string{"infra"})
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}

		entry, err := Bundle(root, files)
		if err != nil {
			t.Fatalf("Bundle: %v", err)
		}

		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		cmd := exec.Command("node", entry)
		cmd.Env = append(cmd.Environ(), "OCEL_DEV_SERVER="+server.URL)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("run bundled entry: %v\n%s", err, out)
		}

		if !strings.Contains(string(out), "declared") {
			t.Errorf("output = %s, want the imported file to have run", out)
		}
		if got := atomic.LoadInt32(&calls); got != 0 {
			t.Fatalf("requests = %d, want the bundle to post nothing of its own", got)
		}
	})

	t.Run("runs CJS deps that require node builtins", func(t *testing.T) {
		root := t.TempDir()
		write(t, filepath.Join(root, "node_modules", "cjsdep", "package.json"),
			`{"name": "cjsdep", "version": "1.0.0", "main": "index.js"}`)
		write(t, filepath.Join(root, "node_modules", "cjsdep", "index.js"),
			`const { EventEmitter } = require("events");
module.exports = { emitter: new EventEmitter() };
`)
		write(t, filepath.Join(root, "infra", "main.ts"), `import "cjsdep";`+registerSideEffect)

		files, err := Discover(root, []string{"infra"})
		if err != nil {
			t.Fatalf("Discover: %v", err)
		}

		entry, err := Bundle(root, files)
		if err != nil {
			t.Fatalf("Bundle: %v", err)
		}

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		cmd := exec.Command("node", entry)
		cmd.Env = append(cmd.Environ(), "OCEL_DEV_SERVER="+server.URL)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("run bundled entry with CJS builtin-requiring dep: %v\n%s", err, out)
		}
	})
}
