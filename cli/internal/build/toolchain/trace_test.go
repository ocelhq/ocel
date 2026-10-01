package toolchain

import (
	"path/filepath"
	"strings"
	"testing"
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

	t.Run("refuses an app whose discovery roots declare a worker", func(t *testing.T) {
		t.Parallel()

		target := Target{App: "api", Source: source, Entrypoint: filepath.Join(source, "server.js"), WorkerSource: "await import('./tasks.js');\n"}
		_, err := target.TracedHandler()
		if err == nil || !strings.Contains(err.Error(), "worker") || !strings.Contains(err.Error(), PreferTracingEnv) {
			t.Errorf("TracedHandler err = %v, want a refusal naming the worker and %s", err, PreferTracingEnv)
		}
	})
}
