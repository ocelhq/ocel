package nodeprotocol

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/obs"
)

func newRun(t *testing.T) (context.Context, *obs.Run) {
	t.Helper()
	ctx, run, err := obs.Start(context.Background(), t.TempDir(), "ocel build")
	if err != nil {
		t.Fatalf("obs.Start: %v", err)
	}
	t.Cleanup(func() { _ = run.Close() })
	return ctx, run
}

func readTrace(t *testing.T, run *obs.Run) string {
	t.Helper()
	raw, err := os.ReadFile(strings.TrimSuffix(run.LogPath(), ".ndjson") + ".otlp.json")
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	return string(raw)
}

func readLog(t *testing.T, run *obs.Run) string {
	t.Helper()
	raw, err := os.ReadFile(run.LogPath())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(raw)
}

func TestProcessorForwardsNonProtocolOutput(t *testing.T) {
	ctx, run := newRun(t)
	var out strings.Builder
	p := &Processor{Run: run, Forward: &out}

	p.Scan(ctx, strings.NewReader("Compiled successfully\n{\"unrelated\":\"json\"}\n"))

	if got := out.String(); got != "Compiled successfully\n{\"unrelated\":\"json\"}\n" {
		t.Errorf("forwarded output = %q, want the input echoed verbatim", got)
	}
}

func TestProcessorForwardsAMalformedProtocolLineVerbatim(t *testing.T) {
	ctx, run := newRun(t)
	var out strings.Builder
	p := &Processor{Run: run, Forward: &out}

	line := Prefix + "{not json"
	p.Scan(ctx, strings.NewReader(line+"\n"))

	if got := out.String(); got != line+"\n" {
		t.Errorf("forwarded output = %q, want the malformed protocol line forwarded, not swallowed", got)
	}
}

func TestProcessorEmitsASpanPerApp(t *testing.T) {
	ctx, run := newRun(t)
	p := &Processor{Run: run}

	send(p, ctx, record{Type: typeSpanStart, ID: "1", App: "api", Stage: "build"})
	send(p, ctx, record{Type: typeSpanStart, ID: "2", App: "worker", Stage: "build"})
	ok := true
	send(p, ctx, record{Type: typeSpanEnd, ID: "1", OK: &ok})
	send(p, ctx, record{Type: typeSpanEnd, ID: "2", OK: &ok})

	if err := run.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	trace := readTrace(t, run)
	if strings.Count(trace, `"name": "build"`) != 2 {
		t.Errorf("trace = %s, want two spans named \"build\", one per app", trace)
	}
	if !strings.Contains(trace, `"stringValue": "api"`) || !strings.Contains(trace, `"stringValue": "worker"`) {
		t.Errorf("trace = %s, want each span attributed to its own app", trace)
	}
}

func TestProcessorMarksAFailedSpanWithoutLeakingTheErrorText(t *testing.T) {
	ctx, run := newRun(t)
	p := &Processor{Run: run}

	send(p, ctx, record{Type: typeSpanStart, ID: "1", App: "api", Stage: "build"})
	send(p, ctx, record{Type: typeError, App: "api", Stage: "build", Message: "sk_live_topsecret build failed"})
	notOK := false
	send(p, ctx, record{Type: typeSpanEnd, ID: "1", OK: &notOK})

	if err := run.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	trace := readTrace(t, run)
	if !strings.Contains(trace, `"code": "STATUS_CODE_ERROR"`) {
		t.Errorf("trace = %s, want the span status marked as an error", trace)
	}
	if strings.Contains(trace, "sk_live_topsecret") {
		t.Errorf("trace = %s, the raw error message must never reach the trace artifact", trace)
	}

	log := readLog(t, run)
	if !strings.Contains(log, "sk_live_topsecret build failed") {
		t.Errorf("log = %s, want the actual error message on the human log", log)
	}
}

func TestProcessorAbortEndsAnyOpenSpan(t *testing.T) {
	ctx, run := newRun(t)
	p := &Processor{Run: run}

	send(p, ctx, record{Type: typeSpanStart, ID: "1", App: "api", Stage: "build"})
	p.Abort()

	if err := run.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	trace := readTrace(t, run)
	if strings.Count(trace, `"name": "build"`) != 1 {
		t.Errorf("trace = %s, want the abandoned span to still appear", trace)
	}
	if !strings.Contains(trace, `"code": "STATUS_CODE_ERROR"`) {
		t.Errorf("trace = %s, want the abandoned span marked as an error", trace)
	}
}

func TestProcessorErrReturnsTheLastErrorRecord(t *testing.T) {
	ctx, run := newRun(t)
	p := &Processor{Run: run}

	send(p, ctx, record{Type: typeError, App: "api", Stage: "build", Message: "no entrypoint resolved"})

	if got, want := p.Err(), "no entrypoint resolved"; got != want {
		t.Errorf("Err() = %q, want %q", got, want)
	}
}

func TestProcessorWorksWithNoRun(t *testing.T) {
	var out strings.Builder
	p := &Processor{Forward: &out}

	line := "plain framework output"
	protocolLine := Prefix + `{"type":"span_start","id":"1","app":"api","stage":"build"}`
	p.Scan(context.Background(), strings.NewReader(line+"\n"+protocolLine+"\n"))

	if got := out.String(); got != line+"\n" {
		t.Errorf("forwarded output = %q, want only the non-protocol line", got)
	}
}

func TestProcessorParsesTheDocumentedWireFormat(t *testing.T) {
	ctx, run := newRun(t)
	var out strings.Builder
	p := &Processor{Run: run, Forward: &out}

	raw, err := json.Marshal(record{Type: typeLog, App: "api", Stage: "build", Level: "info", Message: "installing dependencies"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	p.Scan(ctx, strings.NewReader(Prefix+string(raw)+"\n"))

	if err := run.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	log := readLog(t, run)
	if !strings.Contains(log, "installing dependencies") || !strings.Contains(log, `"app":"api"`) {
		t.Errorf("log = %s, want the log record keyed by app", log)
	}
}

func send(p *Processor, ctx context.Context, rec record) {
	raw, _ := json.Marshal(rec)
	p.line(ctx, Prefix+string(raw))
}

func TestArtifactsLandUnderRunsDir(t *testing.T) {
	_, run := newRun(t)
	if got, want := filepath.Base(filepath.Dir(run.LogPath())), "runs"; got != want {
		t.Errorf("log dir = %q, want %q", got, want)
	}
}
