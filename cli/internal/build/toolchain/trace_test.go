package toolchain

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

func TestTracedHandler(t *testing.T) {
	t.Parallel()

	source := filepath.Join(t.TempDir(), "app")
	for _, tt := range []struct {
		entrypoint string
		want       string
	}{
		{"src/server.ts", "src/server.js"},
		{"src/server.tsx", "src/server.js"},
		{"server.mts", "server.mjs"},
		{"server.cts", "server.cjs"},
		{"src/index.js", "src/index.js"},
		{"app.mjs", "app.mjs"},
	} {
		t.Run(tt.entrypoint+" is served as "+tt.want, func(t *testing.T) {
			t.Parallel()

			got, err := Target{App: "api", Source: source, Entrypoint: filepath.Join(source, filepath.FromSlash(tt.entrypoint))}.TracedHandler()
			if err != nil {
				t.Fatalf("TracedHandler: %v", err)
			}
			if got != tt.want {
				t.Errorf("TracedHandler = %q, want %q", got, tt.want)
			}
		})
	}

	for _, entrypoint := range []string{
		filepath.Join(filepath.Dir(source), "shared", "server.js"),
		filepath.Join(source, "node_modules", "server-pkg", "index.js"),
	} {
		t.Run("refuses "+entrypoint, func(t *testing.T) {
			t.Parallel()

			_, err := Target{App: "api", Source: source, Entrypoint: entrypoint}.TracedHandler()
			if err == nil || !strings.Contains(err.Error(), PreferTracingEnv) {
				t.Errorf("TracedHandler err = %v, want a refusal naming %s", err, PreferTracingEnv)
			}
		})
	}
}

func TestDescribeTraceCarriesTheWorkerEntryOfAnAppThatDeclaresWorkers(t *testing.T) {
	t.Parallel()

	l := newLayout(t, tree{
		"package.json": appPkg,
		"server.js":    "console.log('served');\n",
		"tasks.js":     "export const shape = 'worker:' + typeof process;\n",
	})
	if err := os.MkdirAll(l.functionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := l.target("server.js")
	target.Source = l.appSrc
	target.WorkerSource = "const { shape } = await import(" + strconv.Quote(filepath.Join(l.appSrc, "tasks.js")) + ");\nconsole.log(shape);\n"
	target.WorkerResolveDir = l.appSrc

	if err := DescribeTrace(context.Background(), target); err != nil {
		t.Fatalf("DescribeTrace: %v", err)
	}

	var cfg buildoutput.FunctionConfig
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(l.functionDir, buildoutput.FunctionConfigFile))), &cfg); err != nil {
		t.Fatal(err)
	}
	if want := []string{"node", "ocel-worker.mjs"}; !slices.Equal(cfg.Worker, want) {
		t.Errorf("%s worker = %v, want %v: a traced function a worker runs from starts the bundled entry", buildoutput.FunctionConfigFile, cfg.Worker, want)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	out, err := exec.Command("node", filepath.Join(l.functionDir, "ocel-worker.mjs")).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "worker:object") {
		t.Errorf("the worker entry printed %q (%v), want the bundled declarations to run", out, err)
	}
}
