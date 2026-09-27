package runui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/pkg/naming"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func startTestRun(t *testing.T, dir, command string) *runtrace.Run {
	t.Helper()
	_, run, err := runtrace.Start(context.Background(), dir, command)
	if err != nil {
		t.Fatalf("runtrace.Start() = %v", err)
	}
	t.Cleanup(func() { _ = run.Close() })
	return run
}

func newTestSession(t *testing.T, command string) (*Session, *safeBuffer, string) {
	t.Helper()
	dir := t.TempDir()
	run := startTestRun(t, dir, command)
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatHuman, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })
	return s, &out, s.LogPath()
}

func newVerboseTestSession(t *testing.T, command string) (*Session, *safeBuffer, string) {
	t.Helper()
	dir := t.TempDir()
	run := startTestRun(t, dir, command)
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatHuman, Verbose: true, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })
	return s, &out, s.LogPath()
}

var testStageID = naming.PhaseID(naming.UnitEnvironment, naming.PhaseProvisioning)

func startScopes(s *Session, scopes ...scope) {
	for _, sc := range scopes {
		s.Event(&progressv1.OperationEvent{Phase: sc.phase, SpanId: sc.id, Message: sc.title, Body: &progressv1.OperationEvent_Started{
			Started: &progressv1.Started{ParentSpanId: sc.parent},
		}})
	}
}

func startProvisioning(s *Session) {
	startScopes(s,
		scope{id: naming.UnitID(naming.UnitEnvironment), title: "Environment"},
		scope{id: testStageID, parent: naming.UnitID(naming.UnitEnvironment), phase: progressv1.Phase_PHASE_PROVISION},
	)
}

func endedOp(id []byte, failed bool, d time.Duration) *progressv1.OperationEvent {
	status := progressv1.SpanStatus_SPAN_STATUS_OK
	if failed {
		status = progressv1.SpanStatus_SPAN_STATUS_ERROR
	}
	return &progressv1.OperationEvent{TimeUnixNano: int64(d) + 1, SpanId: id, Body: &progressv1.OperationEvent_Ended{
		Ended: &progressv1.Ended{Status: status, StartTimeUnixNano: 1},
	}}
}

func endScope(s *Session, id []byte, name string, status progressv1.SpanStatus, attrs []*progressv1.SpanAttribute) {
	startScopes(s, scope{id: id, title: name})
	s.Event(&progressv1.OperationEvent{SpanId: id, Body: &progressv1.OperationEvent_Ended{
		Ended: &progressv1.Ended{Status: status, Attributes: attrs},
	}})
}

func closeProvisioning() *progressv1.OperationEvent {
	return endedOp(testStageID, false, time.Second)
}

func outputOp(id []byte, line string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{SpanId: id, Message: line, Body: &progressv1.OperationEvent_Output{Output: &progressv1.Output{}}}
}

func logLine(msg string) *progressv1.OperationEvent {
	return outputOp(testStageID, msg)
}

func progress(msg string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{Level: progressv1.Level_LEVEL_INFO, SpanId: testStageID, Message: msg}
}

func progressN(msg string, current, total uint32) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{SpanId: testStageID, Message: msg, Body: &progressv1.OperationEvent_Counter{
		Counter: &progressv1.Counter{Current: current, Total: &total},
	}}
}

func liveSession(t *testing.T) (*Session, *safeBuffer) {
	t.Helper()
	run := startTestRun(t, t.TempDir(), "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatHuman, TTY: true, Width: defaultWidth, Height: defaultHeight})
	t.Cleanup(func() { _ = s.Close() })
	return s, &out
}

func TestAnInterruptTakesTheLiveFrameBackAndFlushesWhatWasInFlight(t *testing.T) {
	t.Parallel()

	run := startTestRun(t, t.TempDir(), "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatHuman, TTY: true, Verbose: true, Width: defaultWidth, Height: defaultHeight})
	t.Cleanup(func() { _ = s.Close() })
	startProvisioning(s)
	s.Event(progress("provisioning the account"))
	if _, err := s.ProcessWriter("aws", progressv1.Stream_STREAM_STDOUT).Write([]byte("a line the run never finished")); err != nil {
		t.Fatalf("Write() = %v", err)
	}

	s.interrupt()

	if got := s.stream.r.liveLines; got != 0 {
		t.Errorf("liveLines = %d, want the live frame taken back so no frame is committed to the scrollback", got)
	}
	got := out.String()
	if !strings.Contains(got, "a line the run never finished") {
		t.Errorf("stdout = %q, want the in-flight block flushed by the interrupt", got)
	}
	if !strings.Contains(got, "interrupted") {
		t.Errorf("stdout = %q, want the interrupted marker on the flushed strand", got)
	}
	if !strings.Contains(got, "Cancelled") {
		t.Errorf("stdout = %q, want the run to say where it stopped", got)
	}
}

func TestAnInterruptedRunIsNotCancelledTwiceWhenItsCloseStillRuns(t *testing.T) {
	t.Parallel()

	s, out := liveSession(t)
	s.interrupt()
	atInterrupt := out.String()

	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if got := out.String(); got != atInterrupt {
		t.Errorf("Close() after an interrupt wrote %q more, want the teardown to happen exactly once", got[len(atInterrupt):])
	}
}

func TestInterruptReachesEveryRunUIStillOwningATerminal(t *testing.T) {
	s, out := liveSession(t)
	startProvisioning(s)
	s.Event(progress("provisioning the account"))

	Interrupt()

	if got := s.stream.r.liveLines; got != 0 {
		t.Errorf("liveLines = %d, want the exit path to reach the live run-UI it never got to close", got)
	}
	if !strings.Contains(out.String(), "Cancelled") {
		t.Errorf("stdout = %q, want the interrupted result committed", out.String())
	}
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(raw)
}

func TestSession(t *testing.T) {
	t.Run("progress lands in its flushed block, and content with no stage behind it never reaches stdout", func(t *testing.T) {
		t.Parallel()
		s, out, logPath := newTestSession(t, "ocel deploy")

		startProvisioning(s)
		s.Event(progress("Uploading function artifacts"))
		engine := outputOp(nil, "pulumi engine line")
		engine.Level = progressv1.Level_LEVEL_DEBUG
		s.Event(engine)
		s.Event(closeProvisioning())
		s.Event(&progressv1.OperationEvent{Body: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{
			Success: true,
			Apps:    []*progressv1.AppResult{{App: "web", Urls: []string{"https://app.example.workers.dev"}}},
		}}})
		s.Deployed("Deployed", nil, Flip{}, nil, nil)

		got := out.String()
		for _, want := range []string{
			"Uploading function artifacts",
			"Deployed in",
			"https://app.example.workers.dev",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("stdout = %q, want it to contain %q", got, want)
			}
		}
		if strings.Contains(got, "pulumi engine line") {
			t.Errorf("stdout = %q, want a log line no stage claims kept out of the app-major view", got)
		}

		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		log := readLog(t, logPath)
		for _, want := range []string{"[progress] Uploading function artifacts", "[log] pulumi engine line"} {
			if !strings.Contains(log, want) {
				t.Errorf("log = %q, want it to contain %q", log, want)
			}
		}
	})

	t.Run("a line rewritten with carriage returns arrives as the last thing it said", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out safeBuffer
		s := New(&out, run, Presentation{Format: FormatHuman, Verbose: true, Width: defaultWidth})
		logPath := s.LogPath()

		startProvisioning(s)
		s.Event(logLine("uploading 10%\ruploading 60%\ruploaded"))
		s.Event(progress("provisioning 1%\rprovisioning done"))
		s.Event(logLine("carriage returned\r"))
		s.Event(logLine("first of two\r\nsecond of two"))
		s.Event(closeProvisioning())
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}

		for _, surface := range []struct {
			name string
			text string
		}{
			{"stdout", out.String()},
			{"the log file", readLog(t, logPath)},
		} {
			for _, want := range []string{"uploaded", "provisioning done", "carriage returned", "first of two", "second of two"} {
				if !strings.Contains(surface.text, want) {
					t.Errorf("%s = %q, want %q, the last thing that line said", surface.name, surface.text, want)
				}
			}
			if strings.Contains(surface.text, "60%") || strings.Contains(surface.text, "1%") {
				t.Errorf("%s = %q, want the overwritten drafts gone", surface.name, surface.text)
			}
			if strings.Contains(surface.text, "\r") {
				t.Errorf("%s = %q, want no carriage return left to redraw a line nobody is watching", surface.name, surface.text)
			}
		}
	})

	t.Run("determinate progress is logged with counts", func(t *testing.T) {
		t.Parallel()
		s, _, logPath := newTestSession(t, "ocel deploy")
		s.Event(progressN("Uploading function artifacts", 3, 5))
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}

		if log := readLog(t, logPath); !strings.Contains(log, "(3/5)") {
			t.Errorf("log = %q, want it to record the 3/5 count", log)
		}
	})

	t.Run("fail renders the error and a log pointer", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel deploy")
		s.Building()
		s.Fail(errors.New("creating rds: InsufficientCapacity"))

		got := out.String()
		if !strings.Contains(got, "creating rds: InsufficientCapacity") {
			t.Errorf("stdout = %q, want the error message", got)
		}
		if !strings.Contains(got, ".log") {
			t.Errorf("stdout = %q, want a pointer to the log file", got)
		}
	})

	t.Run("fail lists what a slow delete left in place, one item to a line", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel destroy production")
		s.Fail(&edge.OutstandingError{
			Because: "API Gateway paces deletions",
			Waited:  14*time.Minute + 30*time.Second,
			Items: []edge.Outstanding{
				{Kind: "REST API", Name: "api1"},
				{Kind: "REST API", Name: "api2"},
			},
		})

		got := out.String()
		for _, want := range []string{"re-run the same command", "• REST API api1", "• REST API api2"} {
			if !strings.Contains(got, want) {
				t.Errorf("stdout = %q, want it to contain %q", got, want)
			}
		}
		if strings.Contains(got, "api1  • REST API api2") {
			t.Errorf("stdout = %q, want each outstanding item on its own line", got)
		}
	})

	t.Run("fail with no active step still prints a failure line", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel deploy")
		s.Fail(errors.New("boom"))

		if !strings.Contains(out.String(), "Failed") {
			t.Errorf("stdout = %q, want a bare Failed line", out.String())
		}
	})

	t.Run("cancel warns about partial state and hints at reconciling", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel deploy")
		s.Event(progress("Provisioning resources"))
		s.Cancel()

		got := out.String()
		for _, want := range []string{"Cancelled", "partially created", "ocel deploy"} {
			if !strings.Contains(got, want) {
				t.Errorf("stdout = %q, want it to contain %q", got, want)
			}
		}
		if !strings.Contains(got, warnMark+" Cancelled") {
			t.Errorf("stdout = %q, want an interruption marked as one rather than as a failure", got)
		}
	})

	t.Run("waiting prints where to go and how to abort", func(t *testing.T) {
		t.Parallel()
		s, out, logPath := newTestSession(t, "ocel deploy")
		s.Waiting(missingStripeKey(), "http://127.0.0.1:5555/#t=abc")

		got := out.String()
		for _, want := range []string{"STRIPE_API_KEY", "http://127.0.0.1:5555/#t=abc", "Ctrl-C"} {
			if !strings.Contains(got, want) {
				t.Errorf("stdout = %q, want it to contain %q", got, want)
			}
		}
		if raw, err := os.ReadFile(logPath); err == nil && !strings.Contains(string(raw), "waiting") {
			t.Errorf("log = %s, want the wait recorded", raw)
		}
	})

	t.Run("cancel while waiting does not warn about resources that cannot exist", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel deploy")
		s.Waiting(missingStripeKey(), "http://127.0.0.1:5555/#t=abc")
		s.Cancel()

		got := out.String()
		if strings.Contains(got, "Resources may be partially created") {
			t.Errorf("stdout = %q, want no partial-provisioning warning for a run cancelled before provisioning", got)
		}
		if !strings.Contains(got, "Nothing has been provisioned") {
			t.Errorf("stdout = %q, want the cancel to say nothing was provisioned", got)
		}
	})

	t.Run("waiting never persists the session token to the log", func(t *testing.T) {
		t.Parallel()
		const token = "s3cr3t-session-token"
		s, out, logPath := newTestSession(t, "ocel deploy")
		s.Waiting(missingStripeKey(), "http://127.0.0.1:41234/#t="+token)
		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}

		log := readLog(t, logPath)
		if strings.Contains(log, token) {
			t.Errorf("log = %q, want the session token never persisted", log)
		}
		if !strings.Contains(log, "[waiting] http://127.0.0.1:41234/") {
			t.Errorf("log = %q, want the wait recorded with the address", log)
		}
		if !strings.Contains(out.String(), token) {
			t.Errorf("stdout = %q, want the full URL on screen", out.String())
		}
	})

	t.Run("cancel after resume warns about resources again", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel deploy")
		s.Waiting(missingStripeKey(), "http://127.0.0.1:5555/#t=abc")
		s.Resume()
		s.Cancel()

		got := out.String()
		if !strings.Contains(got, "Resources may be partially created") {
			t.Errorf("stdout = %q, want a resumed run cancelled later to warn about partial provisioning", got)
		}
	})

	t.Run("a stage plan event is dispatched to the renderer's tree", func(t *testing.T) {
		t.Parallel()
		s, _, _ := newTestSession(t, "ocel deploy")

		build := []byte{1, 0, 0, 0, 0, 0, 0, 0}
		startScopes(s, []scope{{id: build, title: "Building"}}...)

		if title := s.stream.r.plan.nodes[stageKey(build)].title; title != "Building" {
			t.Errorf("stage title = %q, want %q", title, "Building")
		}
	})

	t.Run("a declared stage with no title falls back to its phase label", func(t *testing.T) {
		t.Parallel()
		s, _, _ := newTestSession(t, "ocel deploy")

		stage := []byte{2, 0, 0, 0, 0, 0, 0, 0}
		startScopes(s, []scope{{id: stage, phase: progressv1.Phase_PHASE_PROVISION}}...)

		if title := s.stream.r.plan.nodes[stageKey(stage)].title; title != "Provisioning" {
			t.Errorf("stage title = %q, want the phase label the declaration names", title)
		}
	})

	t.Run("a child stage arriving before its parent still attaches", func(t *testing.T) {
		t.Parallel()
		plan := newStagePlan()
		parent := []byte{9, 0, 0, 0, 0, 0, 0, 0}
		child := []byte{10, 0, 0, 0, 0, 0, 0, 0}

		declareAll(plan, []scope{
			{id: child, parent: parent, title: "app-a"},
		}...)
		if _, ok := plan.nodes[stageKey(child)]; !ok {
			t.Fatal("orphan child was not recorded at all")
		}
		if plan.nodes[stageKey(child)].linked {
			t.Error("orphan child linked before its parent arrived")
		}

		declareAll(plan, []scope{
			{id: parent, title: "apps"},
		}...)

		parentNode := plan.nodes[stageKey(parent)]
		if len(parentNode.children) != 1 || parentNode.children[0] != stageKey(child) {
			t.Errorf("parent.children = %v, want the orphan attached", parentNode.children)
		}
		if !plan.nodes[stageKey(child)].linked {
			t.Error("child was not marked linked once its parent arrived")
		}
	})

	t.Run("each run gets its own log file and old ones are pruned, not truncated", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		var paths []string
		for i := 0; i < 12; i++ {
			_, run, err := runtrace.Start(context.Background(), dir, "ocel deploy")
			if err != nil {
				t.Fatalf("runtrace.Start() = %v", err)
			}
			var out safeBuffer
			s := New(&out, run, Presentation{Format: FormatHuman, Width: defaultWidth})
			s.Building()
			if err := s.Close(); err != nil {
				t.Fatalf("Close() = %v", err)
			}
			if err := run.Close(); err != nil {
				t.Fatalf("run.Close() = %v", err)
			}
			paths = append(paths, s.LogPath())
		}

		for i, p := range paths {
			if i < 2 {
				if _, err := os.Stat(p); !os.IsNotExist(err) {
					t.Errorf("oldest run %d log %s should have been pruned, stat err = %v", i, p, err)
				}
				continue
			}
			if _, err := os.Stat(p); err != nil {
				t.Errorf("run %d log %s should have survived pruning: %v", i, p, err)
			}
		}
		for i := 1; i < len(paths); i++ {
			if paths[i] == paths[i-1] {
				t.Fatalf("two runs shared a log file path: %s", paths[i])
			}
		}
	})
}

func TestAttributeKeyCoversEveryWireValue(t *testing.T) {
	t.Parallel()
	for raw, name := range progressv1.AttributeKey_name {
		k := progressv1.AttributeKey(raw)
		if k == progressv1.AttributeKey_ATTRIBUTE_KEY_UNSPECIFIED {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, ok := attributeKey(k); !ok {
				t.Errorf("attributeKey(%s) = (_, false), want every declared AttributeKey to map somewhere — an unmapped key is silently dropped by spanAttributes, not rejected", name)
			}
		})
	}
}

func TestIngestedSpanResourceIdentityReachesTheTraceFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := startTestRun(t, dir, "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatHuman, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	endScope(s, []byte{1, 2, 3, 4, 5, 6, 7, 8}, "resource operation failed", progressv1.SpanStatus_SPAN_STATUS_ERROR, []*progressv1.SpanAttribute{
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE, Value: "aws:s3:Bucket"},
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME, Value: "uploads"},
	})

	if err := run.Close(); err != nil {
		t.Fatalf("run.Close() = %v", err)
	}

	tracePath := filepath.Join(run.Dir(), run.TraceID()+".otlp.json")
	raw, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	trace := string(raw)
	for _, want := range []string{"ocel.resource_type", "aws:s3:Bucket", "ocel.resource_name", "uploads"} {
		if !strings.Contains(trace, want) {
			t.Errorf("trace file = %s, want it to contain %q — a failing span's resource identity must survive Session.Event -> IngestSpan -> the trace artifact", trace, want)
		}
	}
}

func TestNumericSpanAttributesLandAsIntValueInTheTraceFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := startTestRun(t, dir, "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatHuman, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	endScope(s, []byte{1, 2, 3, 4, 5, 6, 7, 8}, "upload batch", progressv1.SpanStatus_SPAN_STATUS_OK, []*progressv1.SpanAttribute{
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES, Value: "1048576"},
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT, Value: "42"},
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS, Value: "150"},
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RETRY_COUNT, Value: "2"},
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_EXIT_CODE, Value: "1"},
	})

	if err := run.Close(); err != nil {
		t.Fatalf("run.Close() = %v", err)
	}

	attrs := traceSpanAttrs(t, run, "upload batch")
	want := map[string]string{
		"ocel.bytes":          "1048576",
		"ocel.resource_count": "42",
		"ocel.duration_ms":    "150",
		"ocel.retry_count":    "2",
		"ocel.exit_code":      "1",
	}
	for _, a := range attrs {
		wantVal, ok := want[a.Key]
		if !ok {
			continue
		}
		delete(want, a.Key)
		iv, ok := a.Value["intValue"]
		if !ok {
			t.Errorf("attribute %s value = %v, want an intValue, not a stringValue", a.Key, a.Value)
			continue
		}
		if iv != wantVal {
			t.Errorf("attribute %s intValue = %v, want %v", a.Key, iv, wantVal)
		}
	}
	if len(want) != 0 {
		t.Errorf("attributes missing from the trace file: %v", want)
	}
}

func TestNonNumericValueForANumericKeyDegradesToStringValue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := startTestRun(t, dir, "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatHuman, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	endScope(s, []byte{1, 2, 3, 4, 5, 6, 7, 8}, "malformed byte count", progressv1.SpanStatus_SPAN_STATUS_UNSPECIFIED, []*progressv1.SpanAttribute{
		{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES, Value: "not-a-number"},
	})

	if err := run.Close(); err != nil {
		t.Fatalf("run.Close() = %v", err)
	}

	attrs := traceSpanAttrs(t, run, "malformed byte count")
	if len(attrs) != 1 {
		t.Fatalf("got %d attributes, want 1", len(attrs))
	}
	if attrs[0].Key != "ocel.bytes" {
		t.Fatalf("attribute key = %q, want ocel.bytes", attrs[0].Key)
	}
	if sv, ok := attrs[0].Value["stringValue"]; !ok || sv != "not-a-number" {
		t.Errorf("attribute value = %v, want a stringValue of %q, not the attribute dropped or coerced", attrs[0].Value, "not-a-number")
	}
}

type traceAttr struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

func traceSpanAttrs(t *testing.T, run *runtrace.Run, spanName string) []traceAttr {
	t.Helper()
	tracePath := filepath.Join(run.Dir(), run.TraceID()+".otlp.json")
	raw, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}

	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					Name       string      `json:"name"`
					Attributes []traceAttr `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("trace file is not valid OTLP/JSON: %v", err)
	}

	for _, rs := range doc.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				if sp.Name == spanName {
					return sp.Attributes
				}
			}
		}
	}
	t.Fatalf("no span named %q in the trace file", spanName)
	return nil
}

func TestProviderProcessOutputShowsOnlyWhenVerboseAndNeverEntersABlock(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		origin Origin
		shown  bool
	}{
		{"a terminal", Origin{LogFormat: "human", TTY: true}, false},
		{"a terminal with --verbose", Origin{LogFormat: "human", TTY: true, Verbose: true}, true},
		{"a pipe", Origin{LogFormat: "human"}, false},
		{"a pipe with --verbose", Origin{LogFormat: "human", Verbose: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			run := startTestRun(t, dir, "ocel deploy")
			var out safeBuffer
			s := New(&out, run, Resolve(tc.origin))
			t.Cleanup(func() { _ = s.Close() })

			const marker = "raw subprocess output"
			startProvisioning(s)
			s.Event(progress("a line the phase owns"))
			if _, err := s.ProcessWriter("aws", progressv1.Stream_STREAM_STDOUT).Write([]byte(marker + "\n")); err != nil {
				t.Fatalf("Write() = %v", err)
			}
			s.Event(closeProvisioning())

			got := scrollback(out.String())
			if shown := strings.Contains(got, marker+"\n"); shown != tc.shown {
				t.Errorf("stdout = %q, shows the provider process output = %v, want %v: it is debug output", got, shown, tc.shown)
			}
			if strings.Contains(got, blockIndent+marker) {
				t.Errorf("stdout = %q, want global text kept out of the phase block, which belongs to the unit", got)
			}
			if at, block := strings.Index(got, marker), strings.Index(got, blockIndent+"a line the phase owns"); tc.shown && at > block {
				t.Errorf("stdout = %q, want the line committed as it landed, before the block it interrupted flushed", got)
			}
			if log := readLog(t, s.LogPath()); !strings.Contains(log, "aws: "+marker) {
				t.Errorf("log file = %q, want the raw output always recorded under the provider's name regardless of verbosity", log)
			}
		})
	}
}

func TestCarriageReturnProgressWithNoNewlineIsCollapsedRatherThanBuffered(t *testing.T) {
	t.Parallel()

	var emitted []string
	w := &lineWriter{emit: func(line string) { emitted = append(emitted, line) }}
	for i := 0; i < 20000; i++ {
		if _, err := fmt.Fprintf(w, "\rProgress: resolved %d, reused 0, downloaded %d", i, i); err != nil {
			t.Fatalf("Write() = %v", err)
		}
	}
	if len(emitted) != 0 {
		t.Errorf("the writer emitted %d lines, want a repainted line buffered until it is finished", len(emitted))
	}
	if len(w.pending) > 128 {
		t.Errorf("the writer buffers %d bytes of one repainted line, want only the draft still on screen", len(w.pending))
	}

	w.flush()
	if len(emitted) != 1 {
		t.Fatalf("flush emitted %d lines, want the one draft that survived the repaints", len(emitted))
	}
	if got, want := collapseRewrites(emitted[0]), "Progress: resolved 19999, reused 0, downloaded 19999"; got != want {
		t.Errorf("flushed line = %q, want %q", got, want)
	}
}

func TestALineThatNeverEndsIsCutRatherThanBufferedForever(t *testing.T) {
	t.Parallel()

	var emitted []string
	w := &lineWriter{emit: func(line string) { emitted = append(emitted, line) }}
	for i := 0; i < 64; i++ {
		if _, err := w.Write(bytes.Repeat([]byte("x"), 4096)); err != nil {
			t.Fatalf("Write() = %v", err)
		}
	}
	if len(emitted) == 0 {
		t.Fatalf("a subprocess wrote 256KiB with no line break and nothing was emitted, want the residue bounded")
	}
	if len(w.pending) > maxBufferedLine {
		t.Errorf("the writer buffers %d bytes, want at most %d", len(w.pending), maxBufferedLine)
	}
}

func TestABuildRepaintingOneLineCommitsOnlyTheDraftItLeft(t *testing.T) {
	t.Parallel()
	s, out, _ := newVerboseTestSession(t, "ocel deploy")

	s.Building()
	for i := 1; i <= 500; i++ {
		if _, err := fmt.Fprintf(s.BuildWriter(), "\rProgress: resolved %d", i); err != nil {
			t.Fatalf("Write() = %v", err)
		}
	}
	s.BuildOK()

	got := out.String()
	if strings.Contains(got, "Progress: resolved 1\n") || strings.Count(got, "Progress: resolved") != 1 {
		t.Errorf("stdout = %q, want the repainted line committed once, as the draft the build left behind", got)
	}
	if !strings.Contains(got, blockIndent+"Progress: resolved 500\n") {
		t.Errorf("stdout = %q, want the last draft inside the build block", got)
	}
}

func TestDiagnosticAlwaysReachesTheTerminalRegardlessOfVerbosity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		verbose bool
	}{
		{"non-verbose", false},
		{"verbose", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			run := startTestRun(t, dir, "ocel deploy")
			var out safeBuffer
			s := New(&out, run, Presentation{Format: FormatHuman, Verbose: tc.verbose})
			t.Cleanup(func() { _ = s.Close() })

			s.Diagnostic("no functions to deploy; deploying infrastructure only")

			if !strings.Contains(out.String(), "no functions to deploy; deploying infrastructure only") {
				t.Errorf("stdout = %q, want the diagnostic always visible", out.String())
			}
		})
	}
}

func TestDiagnosticEmitsAStructuredRecordUnderJSONFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := startTestRun(t, dir, "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	s.Diagnostic("warning: POSTHOG_ID is scoped to /web, which no app binds")

	got := parseNDJSON(t, out.String())
	if len(got) != 1 {
		t.Fatalf("recorded %d envelopes, want 1", len(got))
	}
	if msg := got[0].GetMessage(); msg != "warning: POSTHOG_ID is scoped to /web, which no app binds" {
		t.Errorf("message = %q, want the diagnostic text", msg)
	}
	if level := got[0].GetLevel(); level != progressv1.Level_LEVEL_INFO {
		t.Errorf("level = %v, want INFO", level)
	}
	if got[0].GetBody() != nil {
		t.Errorf("body = %v, want a message-only event", got[0].GetBody())
	}
}

func TestARunsResultIsLeveledByHowTheRunEnded(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		end   func(*Session)
		level progressv1.Level
	}{
		{name: "a finished run", end: func(s *Session) { s.Finish("Done") }, level: progressv1.Level_LEVEL_INFO},
		{name: "a failed run", end: func(s *Session) { s.Fail(errors.New("provision production: AccessDenied")) }, level: progressv1.Level_LEVEL_ERROR},
		{name: "an interrupted run", end: func(s *Session) { s.Cancel() }, level: progressv1.Level_LEVEL_WARN},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := startTestRun(t, t.TempDir(), "ocel deploy")
			var out safeBuffer
			s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
			tc.end(s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}

			got := parseNDJSON(t, out.String())
			if len(got) != 1 || got[0].GetResult() == nil {
				t.Fatalf("recorded %d envelopes, want the one result", len(got))
			}
			if level := got[0].GetLevel(); level != tc.level {
				t.Errorf("the result is %v, want %v", level, tc.level)
			}
		})
	}
}

func TestAWarningIsAWarnEventWithItsTextAsTheMessage(t *testing.T) {
	t.Parallel()
	run := startTestRun(t, t.TempDir(), "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	s.Warning("web: OCEL_API_URL is set in .env but is not declared in ocel.config.ts")

	got := parseNDJSON(t, out.String())
	if len(got) != 1 {
		t.Fatalf("recorded %d envelopes, want 1", len(got))
	}
	if level := got[0].GetLevel(); level != progressv1.Level_LEVEL_WARN {
		t.Errorf("level = %v, want WARN", level)
	}
	if msg := got[0].GetMessage(); msg != "web: OCEL_API_URL is set in .env but is not declared in ocel.config.ts" {
		t.Errorf("message = %q, want the warning text", msg)
	}
}

func TestFormatAxis(t *testing.T) {
	t.Run("json format emits one machine-readable line per event, never the human text", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out safeBuffer
		s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
		t.Cleanup(func() { _ = s.Close() })

		s.Building()
		s.Event(progress("Uploading function artifacts"))
		s.Deployed("Deployed", nil, Flip{}, nil, nil)

		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		if len(lines) != 6 {
			t.Fatalf("got %d stdout lines, want 6 (three started scopes, build progress, progress, deployed): %q", len(lines), out.String())
		}
		for _, line := range lines {
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("line %q is not valid JSON: %v", line, err)
			}
		}
		if got := parseNDJSON(t, out.String()); len(got) != 6 || got[5].GetResult() == nil {
			t.Errorf("stream = %q, want six envelopes ending in the run result", out.String())
		}
		if strings.Contains(lines[4], "\r") || strings.HasPrefix(lines[4], "Uploading") {
			t.Errorf("progress line %q looks like the raw human line, not a JSON record", lines[4])
		}
	})

	t.Run("verbose does not change the output format", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out safeBuffer
		s := New(&out, run, Presentation{Format: FormatJSON, Verbose: true, Width: defaultWidth})
		t.Cleanup(func() { _ = s.Close() })

		s.Event(progress("Building"))

		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err != nil {
			t.Fatalf("verbose=true changed the json format away from valid JSON: %v (stdout = %q)", err, out.String())
		}
	})

	t.Run("json format never enters the live-region view", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		s := New(&safeBuffer{}, run, Presentation{Format: FormatJSON, Width: defaultWidth})
		t.Cleanup(func() { _ = s.Close() })
		if s.stream.r != nil {
			t.Error("json format entered the live-region view, which only makes sense for human output on a terminal")
		}
	})

	t.Run("build output rides the stream as an output line in the build phase", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out safeBuffer
		s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
		t.Cleanup(func() { _ = s.Close() })

		if _, err := io.WriteString(s.BuildWriter(), "webpack compiled\n"); err != nil {
			t.Fatalf("BuildWriter().Write() = %v", err)
		}

		got := parseNDJSON(t, out.String())
		if len(got) != 1 {
			t.Fatalf("recorded %d envelopes, want the build line sent as one", len(got))
		}
		if got[0].GetOutput() == nil || got[0].GetMessage() != "webpack compiled" {
			t.Errorf("event = %s, want the build line as an output line", protojson.Format(got[0]))
		}
		if stageKey(got[0].GetSpanId()) != stageKey(buildStageID) {
			t.Errorf("output scope = %s, want the environment building phase", stageKey(got[0].GetSpanId()))
		}
		if got := readLog(t, s.LogPath()); !strings.Contains(got, "webpack compiled") {
			t.Errorf("log = %q, want the build output recorded in the run log too", got)
		}
	})

	t.Run("default format is human-readable, independent of verbosity", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out safeBuffer
		s := New(&out, run, Presentation{Format: FormatHuman, Verbose: true, Width: defaultWidth})
		t.Cleanup(func() { _ = s.Close() })

		startProvisioning(s)
		s.Event(progress("Building project"))
		s.Event(closeProvisioning())

		if !strings.Contains(out.String(), "Building project") {
			t.Errorf("stdout = %q, want the human-readable line", out.String())
		}
		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err == nil {
			t.Errorf("stdout = %q, want human text, not JSON, when format is human even under verbose", out.String())
		}
	})
}

func TestAnEndedScopeWithoutAUsableEndFallsBackToElapsedWallClock(t *testing.T) {
	t.Parallel()

	stage := []byte{7, 0, 0, 0, 0, 0, 0, 0}
	now := time.Now().UTC()
	start := now.Add(-2 * time.Minute)

	for _, tc := range []struct {
		name string
		end  int64
	}{
		{"missing end", 0},
		{"end before start", start.Add(-time.Minute).UnixNano()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			run := startTestRun(t, dir, "ocel deploy")
			var out safeBuffer
			s := New(&out, run, Presentation{Format: FormatHuman, Width: defaultWidth})
			t.Cleanup(func() { _ = s.Close() })
			s.stream.r.useClock(func() time.Time { return now })

			startScopes(s, scope{id: stage, title: "Provisioning"})
			s.Event(&progressv1.OperationEvent{TimeUnixNano: tc.end, SpanId: stage, Body: &progressv1.OperationEvent_Ended{
				Ended: &progressv1.Ended{Status: progressv1.SpanStatus_SPAN_STATUS_OK, StartTimeUnixNano: start.UnixNano()},
			}})

			got := s.stream.r.plan.nodes[stageKey(stage)].doneDur
			if got < 2*time.Minute || got > 2*time.Minute+time.Second {
				t.Errorf("committed duration = %v, want the 2m the stage actually ran, not a collapsed end", got)
			}
		})
	}
}

func TestBar(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name           string
		current, total uint32
		wantFilled     int
	}{
		{"is empty at zero", 0, 5, 0},
		{"is full at the total", 5, 5, barWidth},
		{"stays full past the total", 10, 5, barWidth},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := bar(tc.current, tc.total)
			if filled := strings.Count(got, "█"); filled != tc.wantFilled {
				t.Errorf("bar(%d,%d) filled = %d, want %d", tc.current, tc.total, filled, tc.wantFilled)
			}
		})
	}
}

func TestEveryEnvironmentBlockNamesThePhaseThatFilledIt(t *testing.T) {
	t.Parallel()
	s, out, _ := newTestSession(t, "ocel deploy")

	uploadStageID := naming.PhaseID(naming.UnitEnvironment, naming.PhaseUploading)

	s.Building()
	s.BuildOK()
	startScopes(s,
		scope{id: naming.UnitID(naming.UnitEnvironment), title: "Environment"},
		scope{id: testStageID, parent: naming.UnitID(naming.UnitEnvironment), title: "Provisioning", phase: progressv1.Phase_PHASE_PROVISION},
		scope{id: uploadStageID, parent: naming.UnitID(naming.UnitEnvironment), title: "Uploading", phase: progressv1.Phase_PHASE_DEPLOY},
	)
	s.Event(progress("provisioning the account"))
	s.Event(endedOp(testStageID, false, time.Second))
	s.Event(&progressv1.OperationEvent{SpanId: uploadStageID, Message: "uploading the bundle"})
	s.Event(endedOp(uploadStageID, false, time.Second))

	got := out.String()
	for _, want := range []string{"Environment › Building", "Environment › Provisioning", "Environment › Uploading"} {
		if !strings.Contains(got, okMark+" "+want+"  ") {
			t.Errorf("transcript = %q, want the block headed %q — the unit reads the same way in every block it closes", got, want)
		}
	}
	if strings.Contains(got, okMark+" Environment  ") {
		t.Errorf("transcript = %q, want no block headed by the unit alone: the Environment unit runs more than one phase", got)
	}
}

func TestASuccessfulBuildKeepsItsRawOutputToTheRunLogUnlessVerboseWasAskedFor(t *testing.T) {
	t.Parallel()
	s, out, logPath := newTestSession(t, "ocel deploy")

	s.Building()
	if _, err := s.BuildWriter().Write([]byte("Packages: +812\n▲ Next.js 15.4.2\n")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	s.BuildOK()

	got := out.String()
	for _, unwanted := range []string{"Packages: +812", "▲ Next.js 15.4.2"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("stdout = %q, want %q left out of the default projection", got, unwanted)
		}
	}
	if !strings.Contains(got, okMark+" Environment › Building  ") {
		t.Errorf("stdout = %q, want the phase still committed with its own line", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if log := readLog(t, logPath); !strings.Contains(log, "Packages: +812") {
		t.Errorf("log = %q, want the build output recorded in the run log", log)
	}
}

func TestASuccessfulBuildCommitsItsWholeOutputInsideTheFlushedBlockWhenVerbose(t *testing.T) {
	t.Parallel()
	s, out, logPath := newVerboseTestSession(t, "ocel deploy")

	s.Building()
	if _, err := s.BuildWriter().Write([]byte("Packages: +812\n▲ Next.js 15.4.2\n")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if _, err := s.BuildWriter().Write([]byte("Route (app)")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	if strings.Contains(out.String(), "Packages: +812") {
		t.Fatalf("stdout = %q, want build output kept in its block until the phase completes", out.String())
	}

	s.BuildOK()

	got := out.String()
	for _, want := range []string{"  Packages: +812", "  ▲ Next.js 15.4.2", "  Route (app)"} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("stdout = %q, want the build line %q flushed inside its block", got, want)
		}
	}
	header := strings.Index(got, startMark+" Environment › Building")
	first := strings.Index(got, "  Packages: +812")
	closed := strings.Index(got, okMark+" Environment › Building  ")
	if header < 0 || closed < header || first < closed {
		t.Errorf("stdout = %q, want the whole build output under the completed-phase line, which follows the phase-start line", got)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if log := readLog(t, logPath); !strings.Contains(log, "Packages: +812") {
		t.Errorf("log = %q, want the build output recorded in the run log too", log)
	}
}

func TestAFailedPhaseShowsItsRawOutputWhateverTheVerbosity(t *testing.T) {
	t.Parallel()

	t.Run("the phase's own span records the failure", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel deploy")

		startProvisioning(s)
		s.Event(logLine("error: creating bucket assets: AccessDenied"))
		s.Event(endedOp(testStageID, true, time.Second))

		if got := out.String(); !strings.Contains(got, blockIndent+"error: creating bucket assets: AccessDenied\n") {
			t.Errorf("stdout = %q, want the failed phase's raw output shown without being asked twice", got)
		}
	})

	t.Run("the run ends before the phase's span arrives", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newTestSession(t, "ocel deploy")

		startProvisioning(s)
		s.Event(logLine("error: creating bucket assets: AccessDenied"))
		s.Fail(errors.New("provision production: AccessDenied"))

		if got := out.String(); !strings.Contains(got, blockIndent+"error: creating bucket assets: AccessDenied\n") {
			t.Errorf("stdout = %q, want the block stranded by the failure to show what it contained", got)
		}
	})
}

func TestABuildLineThatCollapsesToNothingIsNeverEmitted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := startTestRun(t, dir, "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	s.Building()
	if _, err := s.BuildWriter().Write([]byte("\n\r\n\r\rPackages: +812\n\n\r\n")); err != nil {
		t.Fatalf("Write() = %v", err)
	}
	s.BuildOK()

	var messages []string
	for _, ev := range parseNDJSON(t, out.String()) {
		if ev.GetOutput() != nil {
			messages = append(messages, ev.GetMessage())
		}
	}
	if want := []string{"Packages: +812"}; strings.Join(messages, "|") != strings.Join(want, "|") {
		t.Errorf("log events = %q, want only the line that has content: %q", messages, want)
	}
}

func TestSessionProgressIDsMatchTheProvidersOwnDerivation(t *testing.T) {
	t.Parallel()

	if got := stageKey(naming.UnitID(naming.UnitEnvironment)); got != "9f2ecbbdfa2db89d" {
		t.Errorf("environment unit id = %s, want the digest the provider derives for the same canonical name", got)
	}
	if got := stageKey(naming.PhaseID(naming.UnitEnvironment, naming.PhaseProvisioning)); got != "ed0ca2aae3a67905" {
		t.Errorf("environment provisioning phase id = %s, want the digest the provider derives for the same pair", got)
	}
}

func TestAnOrphanLogWaitsForItsStageAndFoldsIntoThatStagesBlock(t *testing.T) {
	t.Parallel()

	t.Run("addressed to a phase declared later", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newVerboseTestSession(t, "ocel deploy")

		s.Event(logLine("the vertex spoke first"))
		if got := out.String(); strings.Contains(got, "the vertex spoke first") {
			t.Fatalf("stdout = %q, want an orphan buffered until its stage is declared, never committed out of band", got)
		}

		startProvisioning(s)
		s.Event(logLine("and again once it was declared"))
		s.Event(closeProvisioning())

		got := out.String()
		for _, want := range []string{blockIndent + "the vertex spoke first", blockIndent + "and again once it was declared"} {
			if !strings.Contains(got, want+"\n") {
				t.Errorf("stdout = %q, want %q inside the flushed block", got, want)
			}
		}
		if at, closed := strings.Index(got, "the vertex spoke first"), strings.Index(got, okMark+" Environment  "); closed < 0 || at < closed {
			t.Errorf("stdout = %q, want the adopted orphan flushed under its block header rather than before it", got)
		}
	})

	t.Run("addressed to a detail node declared later under a phase", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newVerboseTestSession(t, "ocel deploy")

		vertex := []byte{9, 9, 9, 9, 9, 9, 9, 9}
		s.Event(outputOp(vertex, "[build 6/9] RUN pnpm build"))
		startProvisioning(s)
		startScopes(s, []scope{
			{id: vertex, parent: testStageID, title: "RUN pnpm build"},
		}...)
		s.Event(closeProvisioning())

		if got := out.String(); !strings.Contains(got, blockIndent+"[build 6/9] RUN pnpm build\n") {
			t.Errorf("stdout = %q, want the orphan folded into the block of the phase its stage turned out to sit under", got)
		}
	})
}

func TestAnOrphanWhoseStageIsNeverDeclaredNeverCommits(t *testing.T) {
	t.Parallel()
	s, out, logPath := newTestSession(t, "ocel deploy")

	s.Event(outputOp([]byte{1, 2, 3, 4, 5, 6, 7, 8}, "a stage nothing ever declared"))
	startProvisioning(s)
	s.Event(closeProvisioning())
	s.Deployed("Deployed", nil, Flip{}, nil, nil)

	got := out.String()
	if strings.Contains(got, "a stage nothing ever declared") {
		t.Errorf("stdout = %q, want the orphan kept out of the human projection — nothing commits out of band", got)
	}
	if log := readLog(t, logPath); !strings.Contains(log, "a stage nothing ever declared") {
		t.Errorf("log = %q, want the orphan recorded even though it never reaches a block", log)
	}
}

func TestABlockCommitsItsLinesVerbatimRightHandWhitespaceIncluded(t *testing.T) {
	t.Parallel()
	s, out, _ := newVerboseTestSession(t, "ocel deploy")

	const padded = "Route (app)                     Size     First Load JS   "
	startProvisioning(s)
	s.Event(logLine(padded))
	s.Event(logLine(""))
	s.Event(closeProvisioning())

	got := out.String()
	if !strings.Contains(got, blockIndent+padded+"\n") {
		t.Errorf("stdout = %q, want the line as the stream sent it — only carriage returns collapse, and output is complete on success", got)
	}
	if strings.Contains(got, "\n"+blockIndent+"\n") {
		t.Errorf("stdout = %q, want an empty line dropped rather than committed as a bare indent", got)
	}
}

func TestAPausedBuildResumesAsAFreshPhase(t *testing.T) {
	t.Parallel()

	t.Run("the stream starts the build phase again after the resume", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out safeBuffer
		s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
		t.Cleanup(func() { _ = s.Close() })

		s.Building()
		s.Waiting(missingStripeKey(), "http://127.0.0.1:5555/#t=abc")
		s.Resume()
		s.BuildOK()

		var order []string
		for _, ev := range parseNDJSON(t, out.String()) {
			switch {
			case ev.GetWaiting() != nil:
				order = append(order, "waiting")
			case ev.GetResumed() != nil:
				order = append(order, "resumed")
			case ev.GetStarted() != nil && bytes.Equal(ev.GetSpanId(), buildStageID):
				order = append(order, "build phase started")
			case ev.GetEnded() != nil && bytes.Equal(ev.GetSpanId(), buildStageID):
				order = append(order, "build phase ended")
			}
		}

		want := []string{"build phase started", "waiting", "resumed", "build phase started", "build phase ended"}
		if strings.Join(order, ", ") != strings.Join(want, ", ") {
			t.Errorf("stream = %v, want %v", order, want)
		}
	})

	t.Run("the resumed build ends marked with the retry count", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out safeBuffer
		s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
		t.Cleanup(func() { _ = s.Close() })

		s.Building()
		s.Waiting(missingStripeKey(), "http://127.0.0.1:5555/#t=abc")
		s.Resume()
		s.BuildOK()

		var ends []*progressv1.Ended
		for _, ev := range parseNDJSON(t, out.String()) {
			if ev.GetEnded() != nil && bytes.Equal(ev.GetSpanId(), buildStageID) {
				ends = append(ends, ev.GetEnded())
			}
		}
		if len(ends) != 1 {
			t.Fatalf("stream ends the build %d times, want once, when the resumed phase ends", len(ends))
		}
		attrs := ends[0].GetAttributes()
		if len(attrs) != 1 || attrs[0].GetKey() != progressv1.AttributeKey_ATTRIBUTE_KEY_RETRY_COUNT || attrs[0].GetValue() != "1" {
			t.Errorf("the build ends with %d attributes, want only the retry count, at 1", len(attrs))
		}
	})

	t.Run("the transcript reads build, waiting card, build again", func(t *testing.T) {
		t.Parallel()
		s, out, _ := newVerboseTestSession(t, "ocel deploy")

		s.Building()
		fmt.Fprintln(s.BuildWriter(), "Reading ocel.aws.config.ts")
		s.Waiting(missingStripeKey(), "http://127.0.0.1:5555/#t=abc")
		s.Resume()
		fmt.Fprintln(s.BuildWriter(), "Compiled successfully")
		s.BuildOK()

		got := out.String()
		var at int
		for _, want := range []string{
			warnMark + " Environment › Building paused\n",
			blockIndent + "Reading ocel.aws.config.ts\n",
			"http://127.0.0.1:5555/#t=abc",
			startMark + " Environment › Building\n",
			okMark + " Environment › Building  ",
			blockIndent + "Compiled successfully\n",
		} {
			i := strings.Index(got[at:], want)
			if i < 0 {
				t.Fatalf("transcript has no %q at or after offset %d:\n%s", want, at, got)
			}
			at += i + len(want)
		}
	})
}

func missingStripeKey() *streamv1.MissingVariables {
	return &streamv1.MissingVariables{
		Cells:  []*streamv1.MissingVariable{{Key: "STRIPE_API_KEY", Reason: "no value"}},
		Remedy: "ocel env ui",
	}
}

func TestAProviderEventKeepsItsEnvelopeOnTheRunsStream(t *testing.T) {
	t.Parallel()
	run := startTestRun(t, t.TempDir(), "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	at := time.Date(2026, 9, 27, 6, 0, 0, 0, time.UTC)
	s.Event(&progressv1.OperationEvent{
		TimeUnixNano: at.UnixNano(),
		Level:        progressv1.Level_LEVEL_WARN,
		Phase:        progressv1.Phase_PHASE_DEPLOY,
		Subject:      "web",
		Message:      "Uploading 3 function artifacts",
		SpanId:       appStage(1),
		Body:         &progressv1.OperationEvent_Counter{Counter: &progressv1.Counter{Current: 1, Total: u32(3)}},
	})

	got := parseNDJSON(t, out.String())
	if len(got) != 1 {
		t.Fatalf("recorded %d envelopes, want 1", len(got))
	}
	ev := got[0]
	if ev.GetPhase() != progressv1.Phase_PHASE_DEPLOY || ev.GetSubject() != "web" {
		t.Errorf("phase, subject = %v, %q, want the provider's deploy phase and app", ev.GetPhase(), ev.GetSubject())
	}
	if ev.GetLevel() != progressv1.Level_LEVEL_WARN || ev.GetMessage() != "Uploading 3 function artifacts" {
		t.Errorf("level, message = %v, %q, want the provider's", ev.GetLevel(), ev.GetMessage())
	}
	if !ev.GetTime().AsTime().Equal(at) {
		t.Errorf("time = %v, want the provider's stamp %v", ev.GetTime().AsTime(), at)
	}
	if !bytes.Equal(ev.GetSpanId(), appStage(1)) {
		t.Errorf("span id = %x, want the provider's %x", ev.GetSpanId(), appStage(1))
	}
	if c := ev.GetCounter(); c.GetCurrent() != 1 || c.GetTotal() != 3 {
		t.Errorf("counter = %d/%d, want the provider's 1/3 as the run event's own body", c.GetCurrent(), c.GetTotal())
	}
}

func TestAProvidersMessageOnlyWarningIsAWarnLineOnTheTerminalAndInTheRunLog(t *testing.T) {
	t.Parallel()
	s, out, logPath := newTestSession(t, "ocel deploy")

	s.Event(&progressv1.OperationEvent{
		Level:   progressv1.Level_LEVEL_WARN,
		Phase:   progressv1.Phase_PHASE_CHECK,
		Subject: "relay",
		Message: "this deploy could not confirm the account may run code at the relay edge",
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if got, want := out.String(), "⚠ this deploy could not confirm the account may run code at the relay edge\n"; !strings.HasPrefix(got, want) {
		t.Errorf("terminal = %q, want it to open with %q", got, want)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "could not confirm the account may run code at the relay edge") {
		t.Errorf("run log = %q, want the warning in it", logged)
	}
}

func TestAProvidersMessageOnlyEventIsAMessageOnlyRunEvent(t *testing.T) {
	t.Parallel()
	run := startTestRun(t, t.TempDir(), "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	s.Event(&progressv1.OperationEvent{Level: progressv1.Level_LEVEL_WARN, Subject: "relay", Message: "the plan is unknown"})

	got := parseNDJSON(t, out.String())
	if len(got) != 1 {
		t.Fatalf("recorded %d envelopes, want 1", len(got))
	}
	if got[0].GetBody() != nil {
		t.Errorf("body = %v, want a message-only event", got[0].GetBody())
	}
	if got[0].GetSubject() != "relay" || got[0].GetMessage() != "the plan is unknown" {
		t.Errorf("subject, message = %q, %q, want the provider's", got[0].GetSubject(), got[0].GetMessage())
	}
}

func TestAPlanTheRunShowsIsInThePlanPhase(t *testing.T) {
	t.Parallel()
	run := startTestRun(t, t.TempDir(), "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	s.Plan("Proposed changes to production", &planv1.ChangePlan{Subject: "production"})

	if got := parseNDJSON(t, out.String()); len(got) != 1 || got[0].GetPhase() != progressv1.Phase_PHASE_PLAN {
		t.Errorf("stream = %q, want one plan event in the plan phase", out.String())
	}
}

func TestTheProjectBuildIsAScopeInTheBuildPhaseThatStartsSaysOutputsAndEnds(t *testing.T) {
	t.Parallel()
	run := startTestRun(t, t.TempDir(), "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatJSON, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	s.Building()
	if _, err := io.WriteString(s.BuildWriter(), "webpack compiled\n"); err != nil {
		t.Fatalf("BuildWriter().Write() = %v", err)
	}
	s.BuildOK()

	var got []string
	for _, ev := range parseNDJSON(t, out.String()) {
		if !bytes.Equal(ev.GetSpanId(), buildStageID) {
			continue
		}
		if ev.GetPhase() != progressv1.Phase_PHASE_BUILD {
			t.Errorf("%s is in phase %v, want the build phase", protojson.Format(ev), ev.GetPhase())
		}
		switch {
		case ev.GetStarted() != nil:
			got = append(got, "started "+ev.GetMessage())
		case ev.GetOutput() != nil:
			got = append(got, "output "+ev.GetMessage())
		case ev.GetEnded() != nil:
			got = append(got, "ended "+ev.GetEnded().GetStatus().String())
		case ev.GetBody() == nil:
			got = append(got, "said "+ev.GetMessage())
		}
	}
	want := []string{"started Building", "said Building project", "output webpack compiled", "ended SPAN_STATUS_OK"}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Errorf("the build scope's events = %q, want %q", got, want)
	}
}

type traceSpan struct {
	Name         string      `json:"name"`
	ParentSpanID string      `json:"parentSpanId"`
	Start        string      `json:"startTimeUnixNano"`
	End          string      `json:"endTimeUnixNano"`
	Attributes   []traceAttr `json:"attributes"`
}

func traceSpanNamed(t *testing.T, run *runtrace.Run, name string) traceSpan {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(run.Dir(), run.TraceID()+".otlp.json"))
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []traceSpan `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("trace file is not valid OTLP/JSON: %v", err)
	}
	for _, rs := range doc.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				if sp.Name == name {
					return sp
				}
			}
		}
	}
	t.Fatalf("no span named %q in the trace file:\n%s", name, raw)
	return traceSpan{}
}

func TestAnEndedScopeIsATraceSpanNamedForWhatItStartedAsUnderItsParent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	run := startTestRun(t, dir, "ocel deploy")
	var out safeBuffer
	s := New(&out, run, Presentation{Format: FormatHuman, Width: defaultWidth})
	t.Cleanup(func() { _ = s.Close() })

	unit, phase := []byte{1, 1, 1, 1, 1, 1, 1, 1}, []byte{2, 2, 2, 2, 2, 2, 2, 2}
	s.Event(&progressv1.OperationEvent{SpanId: unit, Message: "web", Body: &progressv1.OperationEvent_Started{Started: &progressv1.Started{}}})
	s.Event(&progressv1.OperationEvent{Phase: progressv1.Phase_PHASE_DEPLOY, SpanId: phase, Message: "Uploading", Body: &progressv1.OperationEvent_Started{
		Started: &progressv1.Started{ParentSpanId: unit},
	}})
	s.Event(&progressv1.OperationEvent{TimeUnixNano: 9_000_000_000, SpanId: phase, Body: &progressv1.OperationEvent_Ended{Ended: &progressv1.Ended{
		Status:            progressv1.SpanStatus_SPAN_STATUS_OK,
		StartTimeUnixNano: 1_000_000_000,
		Attributes:        []*progressv1.SpanAttribute{{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RETRY_COUNT, Value: "2"}},
	}}})
	if err := run.Close(); err != nil {
		t.Fatalf("run.Close() = %v", err)
	}

	got := traceSpanNamed(t, run, "Uploading")
	if got.ParentSpanID != "0101010101010101" {
		t.Errorf("parent span id = %q, want the unit the phase started under", got.ParentSpanID)
	}
	if got.Start != "1000000000" || got.End != "9000000000" {
		t.Errorf("span runs %s..%s, want its started time to the ended event's time", got.Start, got.End)
	}
	if len(got.Attributes) != 1 || got.Attributes[0].Key != "ocel.retry_count" {
		t.Errorf("attributes = %v, want the ended event's retry count", got.Attributes)
	}
}

func TestAnOutputLineAndACounterReachTheRunLog(t *testing.T) {
	t.Parallel()
	s, _, logPath := newTestSession(t, "ocel deploy")

	s.Event(&progressv1.OperationEvent{SpanId: testStageID, Message: "Packages: +812", Body: &progressv1.OperationEvent_Output{
		Output: &progressv1.Output{Stream: progressv1.Stream_STREAM_STDOUT},
	}})
	s.Event(&progressv1.OperationEvent{SpanId: testStageID, Message: "Generating static pages", Body: &progressv1.OperationEvent_Counter{
		Counter: &progressv1.Counter{Current: 28, Total: u32(28)},
	}})
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	log := readLog(t, logPath)
	for _, want := range []string{"[log] Packages: +812\n", "[progress] Generating static pages (28/28)\n"} {
		if !strings.Contains(log, want) {
			t.Errorf("run log = %q, want it to contain %q", log, want)
		}
	}
}
