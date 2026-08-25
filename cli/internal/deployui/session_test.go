package deployui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/obs"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func startTestRun(t *testing.T, dir, command string) *obs.Run {
	t.Helper()
	_, run, err := obs.Start(context.Background(), dir, command)
	if err != nil {
		t.Fatalf("obs.Start() = %v", err)
	}
	t.Cleanup(func() { _ = run.Close() })
	return run
}

func newTestSession(t *testing.T, command string) (*Session, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	run := startTestRun(t, dir, command)
	var out bytes.Buffer
	s := New(&out, run, FormatHuman, false)
	t.Cleanup(func() { _ = s.Close() })
	return s, &out, s.LogPath()
}

func progress(phase progressv1.Phase, msg string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{
		Progress: &progressv1.ProgressEvent{Phase: phase, Message: msg},
	}}
}

func progressN(phase progressv1.Phase, msg string, current, total uint32) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{Event: &progressv1.OperationEvent_Progress{
		Progress: &progressv1.ProgressEvent{Phase: phase, Message: msg, Current: &current, Total: &total},
	}}
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
	t.Run("raw mode streams progress and writes the log, but keeps raw log lines out of non-verbose stdout", func(t *testing.T) {
		t.Parallel()
		s, out, logPath := newTestSession(t, "ocel deploy")

		s.Building()
		s.Event(progress(progressv1.Phase_PHASE_UPLOADING, "Uploading function artifacts"))
		s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Log{
			Log: &progressv1.LogEvent{Message: "pulumi engine line"},
		}})
		s.Deployed("Deployed", []string{"https://app.example.workers.dev"}, "", Flip{}, nil, nil)

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
			t.Errorf("stdout = %q, want raw Log lines held back from the default non-verbose view", got)
		}

		if err := s.Close(); err != nil {
			t.Fatalf("Close() = %v", err)
		}
		log := readLog(t, logPath)
		for _, want := range []string{"[building]", "[uploading]", "[log] pulumi engine line"} {
			if !strings.Contains(log, want) {
				t.Errorf("log = %q, want it to contain %q", log, want)
			}
		}
	})

	t.Run("verbose surfaces raw log lines that non-verbose holds back", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out bytes.Buffer
		s := New(&out, run, FormatHuman, true)
		t.Cleanup(func() { _ = s.Close() })

		s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Log{
			Log: &progressv1.LogEvent{Message: "pulumi engine line"},
		}})

		if !strings.Contains(out.String(), "pulumi engine line") {
			t.Errorf("stdout = %q, want verbose to surface the raw log line", out.String())
		}
	})

	t.Run("determinate progress is logged with counts", func(t *testing.T) {
		t.Parallel()
		s, _, logPath := newTestSession(t, "ocel deploy")
		s.Event(progressN(progressv1.Phase_PHASE_UPLOADING, "Uploading function artifacts", 3, 5))
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

	t.Run("fail lists what a slow delete left standing, one item to a line", func(t *testing.T) {
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
		s.Event(progress(progressv1.Phase_PHASE_PROVISIONING, "Provisioning resources"))
		s.Cancel()

		got := out.String()
		for _, want := range []string{"Cancelled", "partially created", "ocel deploy"} {
			if !strings.Contains(got, want) {
				t.Errorf("stdout = %q, want it to contain %q", got, want)
			}
		}
	})

	t.Run("waiting prints where to go and how to abort", func(t *testing.T) {
		t.Parallel()
		s, out, logPath := newTestSession(t, "ocel deploy")
		s.Waiting("1 variable is not ready — nothing has been built.\n\n  STRIPE_API_KEY (project root)\n", "http://127.0.0.1:5555/#t=abc")

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
		s.Waiting("1 variable is not ready.", "http://127.0.0.1:5555/#t=abc")
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
		s.Waiting("1 variable is not ready.", "http://127.0.0.1:41234/#t="+token)
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
		s.Waiting("1 variable is not ready.", "http://127.0.0.1:5555/#t=abc")
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
		s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{
			StagePlan: &progressv1.StagePlanEvent{
				Stages: []*progressv1.Stage{{Id: build, Title: "Building"}},
				Final:  true,
			},
		}})

		if title := s.r.plan.nodes[stageKey(build)].title; title != "Building" {
			t.Errorf("stage title = %q, want %q", title, "Building")
		}
		if !s.r.plan.final {
			t.Error("plan.final = false after a StagePlanEvent with Final: true")
		}
	})

	t.Run("a child stage arriving before its parent still attaches", func(t *testing.T) {
		t.Parallel()
		plan := newStagePlan()
		parent := []byte{9, 0, 0, 0, 0, 0, 0, 0}
		child := []byte{10, 0, 0, 0, 0, 0, 0, 0}

		plan.apply(&progressv1.StagePlanEvent{Stages: []*progressv1.Stage{
			{Id: child, ParentId: parent, Title: "app-a"},
		}})
		if _, ok := plan.nodes[stageKey(child)]; !ok {
			t.Fatal("orphan child was not recorded at all")
		}
		if plan.nodes[stageKey(child)].linked {
			t.Error("orphan child linked before its parent arrived")
		}

		plan.apply(&progressv1.StagePlanEvent{Stages: []*progressv1.Stage{
			{Id: parent, Title: "apps"},
		}})

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
			_, run, err := obs.Start(context.Background(), dir, "ocel deploy")
			if err != nil {
				t.Fatalf("obs.Start() = %v", err)
			}
			var out bytes.Buffer
			s := New(&out, run, FormatHuman, false)
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
	var out bytes.Buffer
	s := New(&out, run, FormatHuman, false)
	t.Cleanup(func() { _ = s.Close() })

	s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{
		Span: &progressv1.SpanEvent{
			SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8},
			Name:   "resource operation failed",
			Status: progressv1.SpanStatus_SPAN_STATUS_ERROR,
			Attributes: []*progressv1.SpanAttribute{
				{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE, Value: "aws:s3:Bucket"},
				{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME, Value: "uploads"},
			},
		},
	}})

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
	var out bytes.Buffer
	s := New(&out, run, FormatHuman, false)
	t.Cleanup(func() { _ = s.Close() })

	s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{
		Span: &progressv1.SpanEvent{
			SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8},
			Name:   "upload batch",
			Status: progressv1.SpanStatus_SPAN_STATUS_OK,
			Attributes: []*progressv1.SpanAttribute{
				{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES, Value: "1048576"},
				{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT, Value: "42"},
				{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS, Value: "150"},
				{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_RETRY_COUNT, Value: "2"},
				{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_EXIT_CODE, Value: "1"},
			},
		},
	}})

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
	var out bytes.Buffer
	s := New(&out, run, FormatHuman, false)
	t.Cleanup(func() { _ = s.Close() })

	s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{
		Span: &progressv1.SpanEvent{
			SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8},
			Name:   "malformed byte count",
			Attributes: []*progressv1.SpanAttribute{
				{Key: progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES, Value: "not-a-number"},
			},
		},
	}})

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

func traceSpanAttrs(t *testing.T, run *obs.Run, spanName string) []traceAttr {
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

func TestBuildWriterHonoursVerbosityIndependentlyOfLiveness(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		live         bool
		verbose      bool
		wantTerminal bool
	}{
		{"live, non-verbose: log only", true, false, false},
		{"live, verbose: log only (live already implies non-verbose, but the writer must not double up)", true, true, false},
		{"non-live, non-verbose: log only, no firehose to a non-TTY non-verbose terminal", false, false, false},
		{"non-live, verbose: terminal and log", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			r := newRendererForTest(&out, FormatHuman, tc.live, false)

			logFile, err := os.CreateTemp(t.TempDir(), "session-*.log")
			if err != nil {
				t.Fatalf("create log file: %v", err)
			}
			defer logFile.Close()

			s := &Session{r: r, verbose: tc.verbose, log: logFile}
			s.logWriter = &syncFileWriter{f: logFile, mu: &s.logMu}

			const marker = "raw subprocess output"
			if _, err := s.BuildWriter().Write([]byte(marker)); err != nil {
				t.Fatalf("Write() = %v", err)
			}

			if gotTerminal := strings.Contains(out.String(), marker); gotTerminal != tc.wantTerminal {
				t.Errorf("terminal received the marker = %v, want %v (stdout = %q)", gotTerminal, tc.wantTerminal, out.String())
			}

			logRaw, err := os.ReadFile(logFile.Name())
			if err != nil {
				t.Fatalf("read log file: %v", err)
			}
			if !strings.Contains(string(logRaw), marker) {
				t.Errorf("log file = %q, want the raw output always recorded regardless of verbosity", logRaw)
			}
		})
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
			var out bytes.Buffer
			s := New(&out, run, FormatHuman, tc.verbose)
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
	var out bytes.Buffer
	s := New(&out, run, FormatJSON, false)
	t.Cleanup(func() { _ = s.Close() })

	s.Diagnostic("warning: POSTHOG_ID is scoped to /web, which no app binds")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err != nil {
		t.Fatalf("stdout = %q is not JSON: %v", out.String(), err)
	}
	if rec["type"] != "diagnostic" {
		t.Errorf("record type = %v, want %q", rec["type"], "diagnostic")
	}
	if rec["message"] != "warning: POSTHOG_ID is scoped to /web, which no app binds" {
		t.Errorf("record message = %v, want the diagnostic text", rec["message"])
	}
}

func TestFormatAxis(t *testing.T) {
	t.Run("json format emits one machine-readable line per event, never the human text", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out bytes.Buffer
		s := New(&out, run, FormatJSON, false)
		t.Cleanup(func() { _ = s.Close() })

		s.Building()
		s.Event(progress(progressv1.Phase_PHASE_UPLOADING, "Uploading function artifacts"))
		s.Deployed("Deployed", []string{"https://app.example.workers.dev"}, "", Flip{}, nil, nil)

		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("got %d stdout lines, want 3 (building, progress, deployed): %q", len(lines), out.String())
		}
		for _, line := range lines {
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("line %q is not valid JSON: %v", line, err)
			}
			if _, ok := rec["type"]; !ok {
				t.Errorf("line %q has no %q field", line, "type")
			}
		}
		if strings.Contains(lines[1], "\r") || strings.HasPrefix(lines[1], "Uploading") {
			t.Errorf("progress line %q looks like the raw human line, not a JSON record", lines[1])
		}
	})

	t.Run("verbose does not change the output format", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out bytes.Buffer
		s := New(&out, run, FormatJSON, true)
		t.Cleanup(func() { _ = s.Close() })

		s.Event(progress(progressv1.Phase_PHASE_UPLOADING, "Building"))

		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err != nil {
			t.Fatalf("verbose=true changed the json format away from valid JSON: %v (stdout = %q)", err, out.String())
		}
	})

	t.Run("json format never enters the live-region view", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		s := New(&bytes.Buffer{}, run, FormatJSON, false)
		t.Cleanup(func() { _ = s.Close() })
		if s.r.Live() {
			t.Error("json format entered the live-region view, which only makes sense for human output on a terminal")
		}
	})

	t.Run("default format is human-readable, independent of verbosity", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		run := startTestRun(t, dir, "ocel deploy")
		var out bytes.Buffer
		s := New(&out, run, FormatHuman, true)
		t.Cleanup(func() { _ = s.Close() })

		s.Event(progress(progressv1.Phase_PHASE_UPLOADING, "Building project"))

		if !strings.Contains(out.String(), "Building project") {
			t.Errorf("stdout = %q, want the human-readable line", out.String())
		}
		var rec map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &rec); err == nil {
			t.Errorf("stdout = %q, want human text, not JSON, when format is human even under verbose", out.String())
		}
	})
}

func TestSpanWithoutAUsableEndFallsBackToElapsedWallClock(t *testing.T) {
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
			var out bytes.Buffer
			s := New(&out, run, FormatHuman, false)
			t.Cleanup(func() { _ = s.Close() })
			s.r.useClock(func() time.Time { return now })

			s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_StagePlan{
				StagePlan: &progressv1.StagePlanEvent{
					Stages: []*progressv1.Stage{{Id: stage, Title: "Provisioning"}},
					Final:  true,
				},
			}})
			s.Event(&progressv1.OperationEvent{Event: &progressv1.OperationEvent_Span{
				Span: &progressv1.SpanEvent{
					SpanId:            stage,
					Name:              "provision",
					StartTimeUnixNano: start.UnixNano(),
					EndTimeUnixNano:   tc.end,
					Status:            progressv1.SpanStatus_SPAN_STATUS_OK,
				},
			}})

			if got := s.r.plan.nodes[stageKey(stage)].doneDur; got != 2*time.Minute {
				t.Errorf("committed duration = %v, want the 2m the stage actually ran, not a collapsed span end", got)
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

func TestFallbackTitle(t *testing.T) {
	t.Parallel()

	if got := fallbackTitle(progressv1.Phase_PHASE_PROVISIONING, "Preparing deployment stack"); got != "Provisioning" {
		t.Errorf("fallbackTitle(PROVISIONING, ...) = %q, want the phase label", got)
	}
	if got := fallbackTitle(progressv1.Phase_PHASE_UNSPECIFIED, "Ensuring passphrase"); got != "Ensuring passphrase" {
		t.Errorf("fallbackTitle(UNSPECIFIED, ...) = %q, want the message itself", got)
	}
}
