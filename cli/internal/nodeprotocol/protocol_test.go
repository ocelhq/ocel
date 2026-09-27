package nodeprotocol

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/runtrace"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func newRun(t *testing.T) (context.Context, *runtrace.Run) {
	t.Helper()
	ctx, run, err := runtrace.Start(context.Background(), t.TempDir(), "ocel build")
	if err != nil {
		t.Fatalf("runtrace.Start: %v", err)
	}
	t.Cleanup(func() { _ = run.Close() })
	return ctx, run
}

func readTrace(t *testing.T, run *runtrace.Run) string {
	t.Helper()
	raw, err := os.ReadFile(strings.TrimSuffix(run.LogPath(), ".ndjson") + ".otlp.json")
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	return string(raw)
}

func loggedEvents(t *testing.T, run *runtrace.Run) []*streamv1.RunEvent {
	t.Helper()
	var out []*streamv1.RunEvent
	for _, line := range strings.Split(strings.TrimSpace(readLog(t, run)), "\n") {
		if line == "" {
			continue
		}
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("log line %q is not a run event: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func readLog(t *testing.T, run *runtrace.Run) string {
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
	p.Abort(context.Background())

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

type failingReader struct {
	lines string
	err   error
}

func (r *failingReader) Read(b []byte) (int, error) {
	if r.lines == "" {
		return 0, r.err
	}
	n := copy(b, r.lines)
	r.lines = r.lines[n:]
	return n, nil
}

func TestABuildStillOpenWhenTheBuildersOutputCannotBeReadEndsWithTheReadError(t *testing.T) {
	var ended error
	p := &Processor{AppBuild: func(string) func(error) { return func(err error) { ended = err } }}
	unread := errors.New("the pipe broke")

	p.Scan(context.Background(), &failingReader{
		lines: Prefix + `{"type":"span_start","id":"1","stage":"build","app":"web"}` + "\n",
		err:   unread,
	})
	if err := p.Abort(context.Background()); !errors.Is(err, unread) {
		t.Errorf("Abort() = %v, want the read error", err)
	}
	if !errors.Is(ended, unread) {
		t.Errorf("web's build ended with %v, want the read error that cut the builder's output short", ended)
	}
}

func TestABuildStillOpenWhenTheRunIsCancelledEndsSayingItWasCancelled(t *testing.T) {
	var ended error
	p := &Processor{AppBuild: func(string) func(error) { return func(err error) { ended = err } }}
	send(p, context.Background(), record{Type: typeSpanStart, ID: "1", App: "web", Stage: "build"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.Abort(ctx)
	if ended == nil || ended.Error() != "cancelled before this app's build ended" {
		t.Errorf("web's build ended with %v, want it to say it was cancelled", ended)
	}
}

func TestAbortReportsNothingWhenNoBuildIsOpen(t *testing.T) {
	p := &Processor{AppBuild: func(string) func(error) { return func(error) {} }}
	send(p, context.Background(), record{Type: typeSpanStart, ID: "1", App: "web", Stage: "build"})
	send(p, context.Background(), record{Type: typeSpanEnd, ID: "1", OK: new(true)})
	if err := p.Abort(context.Background()); err != nil {
		t.Errorf("Abort() = %v, want nil: every build ended", err)
	}
}

func TestProcessorErrReturnsTheLastErrorRecord(t *testing.T) {
	ctx, run := newRun(t)
	p := &Processor{Run: run}

	send(p, ctx, record{Type: typeError, App: "api", Stage: "build", Message: "no entrypoint resolved"})

	if got, want := p.Failure(), "no entrypoint resolved"; got != want {
		t.Errorf("Err() = %q, want %q", got, want)
	}
}

func TestScanNeverHangsOnALineLargerThanTheBuffer(t *testing.T) {
	t.Parallel()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	size := maxLineBytes + 1024*1024
	payload := bytes.Repeat([]byte("x"), size)

	writeErr := make(chan error, 1)
	go func() {
		_, werr := pw.Write(payload)
		pw.Close()
		writeErr <- werr
	}()

	var out bytes.Buffer
	p := &Processor{Forward: &out}

	scanDone := make(chan struct{})
	go func() {
		p.Scan(context.Background(), pr)
		close(scanDone)
	}()

	select {
	case <-scanDone:
	case <-time.After(10 * time.Second):
		t.Fatal("Scan did not return: the writer is deadlocked on a full, undrained pipe")
	}

	select {
	case werr := <-writeErr:
		if werr != nil {
			t.Fatalf("write: %v", werr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the pipe writer never returned: a real subprocess would be blocked forever")
	}

	if out.Len() < size {
		t.Errorf("forwarded %d bytes, want at least the full %d-byte line", out.Len(), size)
	}
}

func TestProcessorRecoversARecordGluedToAPrecedingUnterminatedLine(t *testing.T) {
	ctx, run := newRun(t)
	var out strings.Builder
	p := &Processor{Run: run, Forward: &out}

	raw, err := json.Marshal(record{Type: typeError, App: "api", Stage: "build", Message: "no entrypoint resolved"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	p.Scan(ctx, strings.NewReader("Compiling..."+"\n"+Prefix+string(raw)+"\n"))

	if got, want := p.Failure(), "no entrypoint resolved"; got != want {
		t.Errorf("Err() = %q, want %q (a record led by its own newline must survive a preceding partial line)", got, want)
	}
	if !strings.Contains(out.String(), "Compiling...") {
		t.Errorf("forwarded output = %q, want the builder's partial line preserved", out.String())
	}
}

func TestProcessorRejectsARecordWithAnUnrecognisedStage(t *testing.T) {
	ctx, run := newRun(t)
	var out strings.Builder
	p := &Processor{Run: run, Forward: &out}

	line := Prefix + `{"type":"span_start","id":"1","app":"api","stage":"pwned; </trace-span-injection>"}`
	p.Scan(ctx, strings.NewReader(line+"\n"))

	if got := out.String(); got != line+"\n" {
		t.Errorf("forwarded output = %q, want the record with an unrecognised stage forwarded, not recorded", got)
	}

	if err := run.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	trace := readTrace(t, run)
	if strings.Contains(trace, "pwned") {
		t.Errorf("trace = %s, want no span from an unrecognised stage", trace)
	}
}

func TestProcessorCapsTheAppLength(t *testing.T) {
	ctx, run := newRun(t)
	p := &Processor{Run: run}

	longApp := strings.Repeat("a", maxAppLen*2)
	send(p, ctx, record{Type: typeSpanStart, ID: "1", App: longApp, Stage: "build"})
	ok := true
	send(p, ctx, record{Type: typeSpanEnd, ID: "1", OK: &ok})

	if err := run.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	trace := readTrace(t, run)
	if strings.Contains(trace, longApp) {
		t.Errorf("trace = %s, want the app attribute capped at %d bytes", trace, maxAppLen)
	}
}

func TestProcessorParentsALogRecordToItsOpenSpan(t *testing.T) {
	ctx, run := newRun(t)
	p := &Processor{Run: run}

	send(p, ctx, record{Type: typeSpanStart, ID: "1", App: "api", Stage: "build"})
	send(p, ctx, record{Type: typeLog, App: "api", Stage: "build", Level: "info", Message: "installing dependencies"})
	ok := true
	send(p, ctx, record{Type: typeSpanEnd, ID: "1", OK: &ok})

	if err := run.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var spanID string
	for _, ev := range loggedEvents(t, run) {
		if ev.GetMessage() == "installing dependencies" {
			spanID = hex.EncodeToString(ev.GetSpanId())
		}
	}
	if spanID == "" {
		t.Fatalf("log = %s, want the log record to carry a span id", readLog(t, run))
	}

	trace := readTrace(t, run)
	if !strings.Contains(trace, spanID) {
		t.Errorf("trace = %s, want the log's span_id (%s) to match the app's open build span, not the root command span", trace, spanID)
	}
}

func TestProcessorWorksWithNoRun(t *testing.T) {
	var out strings.Builder
	p := &Processor{Forward: &out}

	line := "plain builder output"
	protocolLine := Prefix + `{"type":"span_start","id":"1","app":"api","stage":"build"}`
	p.Scan(context.Background(), strings.NewReader(line+"\n"+protocolLine+"\n"))

	if got := out.String(); got != line+"\n" {
		t.Errorf("forwarded output = %q, want only the non-protocol line", got)
	}
}

func TestANodeBuildLogRecordIsARunEventInTheBuildPhaseNamingItsApp(t *testing.T) {
	ctx, run := newRun(t)
	var out strings.Builder
	p := &Processor{Run: run, Forward: &out}

	raw, err := json.Marshal(record{Type: typeLog, App: "api", Stage: "build", Level: "warn", Message: "installing dependencies"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	p.Scan(ctx, strings.NewReader(Prefix+string(raw)+"\n"))

	if err := run.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	logged := loggedEvents(t, run)
	if len(logged) != 1 {
		t.Fatalf("the log holds %d events, want the one record", len(logged))
	}
	ev := logged[0]
	if ev.GetPhase() != progressv1.Phase_PHASE_BUILD || ev.GetSubject() != "api" || ev.GetLevel() != progressv1.Level_LEVEL_WARN || ev.GetMessage() != "installing dependencies" {
		t.Errorf("logged %s [%s] %s: %q, want WARN [PHASE_BUILD] api: \"installing dependencies\"", ev.GetLevel(), ev.GetPhase(), ev.GetSubject(), ev.GetMessage())
	}
	if ev.GetTime() == nil {
		t.Error("the logged event has no time")
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
