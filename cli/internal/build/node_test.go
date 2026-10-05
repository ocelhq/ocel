package build

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/statedir"

	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
)

func TestAProjectWithNoJavaScriptNeverRunsTheNodeBuildScript(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeGoApp(t, root, "declarations")
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &project.Project{Dir: root}

	ran := false
	builder := nodeOnly{host: servingNext, node: func(context.Context, string, []byte, Log) error {
		ran = true
		return nil
	}}
	if err := builder.Build(context.Background(), cfg, nil, Log{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if ran {
		t.Fatal("the node build script ran for a project with no JavaScript")
	}
}

func TestFailureSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{
			name: "leading warnings do not headline the failure",
			output: ` ⚠ The "middleware" file convention is deprecated. Please use "proxy" instead.
The "id" argument must be of type string. Received undefined
Next.js build worker exited with code: 1 and signal: null
`,
			want: "The \"id\" argument must be of type string. Received undefined\nNext.js build worker exited with code: 1 and signal: null",
		},
		{
			name:   "single line",
			output: "no entrypoint resolved for app \"api\"\n",
			want:   "no entrypoint resolved for app \"api\"",
		},
		{
			name:   "empty output",
			output: "   \n\n",
			want:   "",
		},
		{
			name:   "decorative lines never take the tail",
			output: "Error: adapter threw\n────────────\n   ▲   \n===\n",
			want:   "Error: adapter threw",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := failureSummary(tt.output)
			if got != tt.want {
				t.Errorf("failureSummary() = %q, want %q", got, tt.want)
			}
			if first, _, _ := strings.Cut(got, "\n"); strings.Contains(first, "deprecated") {
				t.Errorf("summary headlines a deprecation warning: %q", first)
			}
		})
	}
}

func runNodeScript(t *testing.T, source string) error {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	path := filepath.Join(t.TempDir(), "builder.mjs")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return runNode(context.Background(), path, []byte(`{"apps":[]}`), Log{})
}

func TestRunNode(t *testing.T) {
	t.Parallel()

	t.Run("a stdout-only failure is not dropped", func(t *testing.T) {
		t.Parallel()

		err := runNodeScript(t, "console.log('adapter could not resolve the entrypoint');process.exit(1);")
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(err.Error(), "adapter could not resolve the entrypoint") {
			t.Errorf("error = %q, want it to include the failure the builder reported on stdout", err)
		}
	})

	t.Run("names the exit code", func(t *testing.T) {
		t.Parallel()

		err := runNodeScript(t, "console.error('boom');process.exit(3);")
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(err.Error(), "3") || !strings.Contains(err.Error(), "boom") {
			t.Errorf("error = %q, want it to name exit status 3 and the failure", err)
		}
	})

	t.Run("a large error record set via process.exitCode is not truncated", func(t *testing.T) {
		t.Parallel()

		message := strings.Repeat("x", 400*1024)
		script := fmt.Sprintf(`console.log(%s + JSON.stringify({type:"error",app:"api",stage:"build",message:%s}));
process.exitCode = 1;
`, jsString(nodeprotocol.Prefix), jsString(message))

		err := runNodeScript(t, script)
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(err.Error(), message) {
			t.Errorf("error has %d bytes, want the full %d-byte record (process.exitCode must not truncate stdout, unlike process.exit)", len(err.Error()), len(message))
		}
	})

	t.Run("a silent failure still errors", func(t *testing.T) {
		t.Parallel()

		err := runNodeScript(t, "process.exit(1);")
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(err.Error(), "the node build failed") {
			t.Errorf("error = %q, want it to name the failing builder", err)
		}
	})

	t.Run("a protocol error record wins over the trailing-lines heuristic", func(t *testing.T) {
		t.Parallel()

		script := fmt.Sprintf(
			`console.log("noise that would otherwise headline the failure");
console.log(%s + JSON.stringify({type:"error",app:"api",stage:"build",message:%s}));
process.exit(1);
`,
			jsString(nodeprotocol.Prefix), jsString(`no entrypoint resolved for app "api"`))

		err := runNodeScript(t, script)
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(err.Error(), `no entrypoint resolved for app "api"`) {
			t.Errorf("error = %q, want the protocol error record's message, not the trailing noise", err)
		}
		if strings.Contains(err.Error(), "noise that would otherwise headline") {
			t.Errorf("error = %q, want the actual error, not the last non-blank lines", err)
		}
	})

	t.Run("a protocol-prefixed line that fails to parse is still forwarded, not swallowed", func(t *testing.T) {
		t.Parallel()

		script := fmt.Sprintf(`console.log(%s + "{not valid json");
process.exit(1);
`, jsString(nodeprotocol.Prefix))

		if _, err := exec.LookPath("node"); err != nil {
			t.Skip("node not on PATH")
		}
		path := filepath.Join(t.TempDir(), "builder.mjs")
		if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		err := runNode(context.Background(), path, []byte(`{"apps":[]}`), Log{Shared: &stderr})
		if err == nil {
			t.Fatal("runNode succeeded on a non-zero exit, want error")
		}
		if !strings.Contains(stderr.String(), nodeprotocol.Prefix+"{not valid json") {
			t.Errorf("stderr = %q, want the malformed protocol line forwarded verbatim", stderr.String())
		}
	})

	t.Run("stdout and stderr write concurrently without racing on the shared writer", func(t *testing.T) {
		t.Parallel()

		script := `
for (let i = 0; i < 4000; i++) {
  process.stdout.write("out " + i + "\n");
  process.stderr.write("err " + i + "\n");
}
`
		if err := runNodeScript(t, script); err != nil {
			t.Fatalf("runNode: %v", err)
		}
	})

	t.Run("a span_start/span_end pair for an app produces a span on the run", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		ctx, building, err := run.NewBus(time.Now).Begin(context.Background(), "ocel build", dir)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		ctx = run.ContextWithSpan(ctx, building.Phase(progressv1.Phase_PHASE_BUILD))

		script := fmt.Sprintf(`const emit = (r) => console.log(%s + JSON.stringify(r));
emit({type:"span_start",id:"1",app:"api",stage:"build"});
emit({type:"span_end",id:"1",ok:true});
`, jsString(nodeprotocol.Prefix))

		if _, lookErr := exec.LookPath("node"); lookErr != nil {
			t.Skip("node not on PATH")
		}
		path := filepath.Join(t.TempDir(), "builder.mjs")
		if writeErr := os.WriteFile(path, []byte(script), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
		if err := runNode(ctx, path, []byte(`{"apps":[]}`), Log{}); err != nil {
			t.Fatalf("runNode: %v", err)
		}
		var ended error
		building.End(&ended)

		traces, err := filepath.Glob(filepath.Join(dir, statedir.Name, "runs", "*.otlp.json"))
		if err != nil || len(traces) != 1 {
			t.Fatalf("traces = %v, %v, want the run's one trace", traces, err)
		}
		raw, err := os.ReadFile(traces[0])
		if err != nil {
			t.Fatalf("read trace: %v", err)
		}
		if !strings.Contains(string(raw), `"name": "build"`) || !strings.Contains(string(raw), `"stringValue": "api"`) {
			t.Errorf("trace = %s, want a build span attributed to app api", raw)
		}
	})
}

func jsString(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(raw)
}
