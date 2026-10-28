package runtrace

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/tracing"
)

func spanOf(r *Trace, id byte, name string, status progressv1.SpanStatus) {
	spanID := []byte{id, 0, 0, 0, 0, 0, 0, 1}
	now := time.Now()
	r.Receive(Call{}, started(spanID, nil, name))
	r.Receive(Call{}, ended(spanID, now, now.Add(time.Second), status))
}

func TestARunKeepsItsEventsAndTraceInADirectoryNamedForWhenItRanItsCommandAndItsTrace(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir, "ocel preview up")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	spanOf(r, 1, "building project", progressv1.SpanStatus_SPAN_STATUS_OK)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	id := r.traceID.String()
	runs, err := os.ReadDir(filepath.Join(dir, buildoutput.StateDir, RunsDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || !runs[0].IsDir() {
		t.Fatalf("the runs directory holds %v, want one directory for the run", runs)
	}
	named := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}Z-preview-up-` + id[:8] + `$`)
	if !named.MatchString(runs[0].Name()) {
		t.Errorf("the run's directory is %q, want <UTC start>-preview-up-%s", runs[0].Name(), id[:8])
	}
	for _, want := range []string{EventsFileName, TraceFileName} {
		if _, err := os.Stat(filepath.Join(dir, buildoutput.StateDir, RunsDirName, runs[0].Name(), want)); err != nil {
			t.Errorf("%s not written: %v", want, err)
		}
	}
}

func TestARunsDirectoryNameSortsByItsUTCStartAndCarriesASafeCommandName(t *testing.T) {
	id := trace.TraceID{0x01, 0x23, 0xab, 0xcd, 0xef}
	eastAfrica := time.FixedZone("EAT", 3*60*60)
	for command, want := range map[string]string{
		"ocel deploy":                       "2026-10-28T09-14-03Z-deploy-0123abcd",
		"ocel preview up":                   "2026-10-28T09-14-03Z-preview-up-0123abcd",
		"ocel bootstrap destroy production": "2026-10-28T09-14-03Z-bootstrap-destroy-production-0123abcd",
		"ocel ../../Etc/Passwd: *now*":      "2026-10-28T09-14-03Z-etc-passwd-now-0123abcd",
		"":                                  "2026-10-28T09-14-03Z-run-0123abcd",
		"ocel " + strings.Repeat("x", 80):   "2026-10-28T09-14-03Z-" + strings.Repeat("x", 32) + "-0123abcd",
	} {
		if got := runDirectoryName(time.Date(2026, 10, 28, 12, 14, 3, 0, eastAfrica), command, id); got != want {
			t.Errorf("runDirectoryName(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestOTLPFileHasARootSpanForTheCommand(t *testing.T) {
	r := startRun(t)
	spanOf(r, 1, "building", progressv1.SpanStatus_SPAN_STATUS_OK)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2 (root + building)", len(spans))
	}
	var root *otlpTestSpan
	for i := range spans {
		if spans[i].ParentSpanId == "" {
			root = &spans[i]
		}
	}
	if root == nil {
		t.Fatal("no root span (a span with no parent) in the trace file")
	}
	if root.Name != "ocel deploy" {
		t.Errorf("root span name = %q, want %q", root.Name, "ocel deploy")
	}
}

func TestAFailedSpansReasonStaysInTheLogAndNeverReachesTheTraceFile(t *testing.T) {
	r := startRun(t)
	const secret = "postgres://user:hunter2@host/db"
	id := []byte{1, 0, 0, 0, 0, 0, 0, 1}
	now := time.Now()
	r.Receive(Call{}, started(id, nil, "provisioning"))
	failed := ended(id, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_ERROR)
	failed.Operation.Message = "dial " + secret + " failed"
	r.Receive(Call{}, failed)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	logRaw, err := os.ReadFile(filepath.Join(r.dir, EventsFileName))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	traceRaw, err := os.ReadFile(filepath.Join(r.dir, TraceFileName))
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	if !strings.Contains(string(logRaw), secret) {
		t.Errorf("log = %s, want the reason on the human log", logRaw)
	}
	if strings.Contains(string(traceRaw), secret) {
		t.Errorf("trace = %s, want no reason text in the trace artifact", traceRaw)
	}
}

type otlpTestDoc struct {
	ResourceSpans []struct {
		ScopeSpans []struct {
			Spans []otlpTestSpan `json:"spans"`
		} `json:"scopeSpans"`
	} `json:"resourceSpans"`
}

type otlpTestSpan struct {
	Name         string `json:"name"`
	TraceId      string `json:"traceId"`
	SpanId       string `json:"spanId"`
	ParentSpanId string `json:"parentSpanId"`
	Start        string `json:"startTimeUnixNano"`
	End          string `json:"endTimeUnixNano"`
	Kind         string `json:"kind"`
	Status       *struct {
		Code string `json:"code"`
	} `json:"status"`
	Attributes []struct {
		Key   string         `json:"key"`
		Value map[string]any `json:"value"`
	} `json:"attributes"`
}

func readTraceDoc(t *testing.T, r *Trace) []otlpTestSpan {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.dir, TraceFileName))
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	var doc otlpTestDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("trace file is not valid OTLP/JSON: %v", err)
	}
	var spans []otlpTestSpan
	for _, rs := range doc.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			spans = append(spans, ss.Spans...)
		}
	}
	return spans
}

func spanNamed(t *testing.T, spans []otlpTestSpan, name string) otlpTestSpan {
	t.Helper()
	for _, s := range spans {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no span named %q in the trace file", name)
	return otlpTestSpan{}
}

var (
	hexTraceID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hexSpanID  = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

func TestOTLPFileUsesHexTraceAndSpanIDs(t *testing.T) {
	r := startRun(t)
	spanOf(r, 1, "building", progressv1.SpanStatus_SPAN_STATUS_OK)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	if len(spans) == 0 {
		t.Fatal("no spans in the trace file")
	}
	for _, s := range spans {
		if s.TraceId != r.traceID.String() {
			t.Errorf("span %q traceId = %q, want %q", s.Name, s.TraceId, r.traceID.String())
		}
		if !hexTraceID.MatchString(s.TraceId) {
			t.Errorf("span %q traceId = %q, want it to match %s", s.Name, s.TraceId, hexTraceID)
		}
		if !hexSpanID.MatchString(s.SpanId) {
			t.Errorf("span %q spanId = %q, want it to match %s", s.Name, s.SpanId, hexSpanID)
		}
	}
}

func TestSpanStatusCodeMapsToTheCorrectOTLPEnum(t *testing.T) {
	r := startRun(t)
	spanOf(r, 1, "ok-span", progressv1.SpanStatus_SPAN_STATUS_OK)
	spanOf(r, 2, "err-span", progressv1.SpanStatus_SPAN_STATUS_ERROR)
	spanOf(r, 3, "unset-span", progressv1.SpanStatus_SPAN_STATUS_UNSPECIFIED)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)

	okSpan := spanNamed(t, spans, "ok-span")
	if okSpan.Status == nil || okSpan.Status.Code != "STATUS_CODE_OK" {
		t.Errorf("ok-span status = %+v, want STATUS_CODE_OK", okSpan.Status)
	}

	errSpan := spanNamed(t, spans, "err-span")
	if errSpan.Status == nil || errSpan.Status.Code != "STATUS_CODE_ERROR" {
		t.Errorf("err-span status = %+v, want STATUS_CODE_ERROR", errSpan.Status)
	}

	unsetSpan := spanNamed(t, spans, "unset-span")
	if unsetSpan.Status != nil && unsetSpan.Status.Code != "" {
		t.Errorf("unset-span status = %+v, want its code unset", unsetSpan.Status)
	}
}

func TestARunsLogAndTraceAreReadableByTheirOwnerAlone(t *testing.T) {
	r := startRun(t)
	spanOf(r, 1, "building project", progressv1.SpanStatus_SPAN_STATUS_OK)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	for path, want := range map[string]os.FileMode{
		filepath.Dir(filepath.Join(r.dir, EventsFileName)): 0o700,
		filepath.Join(r.dir, EventsFileName):               0o600,
		filepath.Join(r.dir, TraceFileName):                0o600,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", path, got, want)
		}
	}
}

func TestTheTraceFileIsWrittenOnceWhenTheRunCloses(t *testing.T) {
	r := startRun(t)
	tracePath := filepath.Join(r.dir, TraceFileName)
	for id := range byte(3) {
		spanOf(r, id+1, "building", progressv1.SpanStatus_SPAN_STATUS_OK)
	}
	if _, err := os.Stat(tracePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat trace before Close = %v, want no file: rewriting it per span grows with the square of the run", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if got := len(readTraceDoc(t, r)); got != 4 {
		t.Errorf("trace holds %d spans, want the root and all 3 ended ones", got)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	s := strings.TrimRight(string(raw), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestASecretValueAddedToARunIsRedactedInItsTraceFileAndKeptInAnotherRuns(t *testing.T) {
	first, second := startRun(t), startRun(t)
	AddSecretValues(first.ContextWithRootSpan(context.Background()), "pw5c")
	for _, r := range []*Trace{first, second} {
		_, span := otel.Tracer("any/package").Start(r.ContextWithRootSpan(context.Background()), "connect")
		span.SetAttributes(attribute.String("process.stdout.tail", "pin pw5c"))
		span.End()
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}

	if doc := readTraceFile(t, first); strings.Contains(doc, "pw5c") {
		t.Errorf("the run's trace file holds the value added to it:\n%s", doc)
	}
	if doc := readTraceFile(t, second); !strings.Contains(doc, "pin pw5c") {
		t.Errorf("another run's trace file lost a value added only to the first:\n%s", doc)
	}
}

func readTraceFile(t *testing.T, r *Trace) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(r.dir, TraceFileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestUnderTraceOffARunWritesItsEventsAndNoTraceAndRecordsNoSpan(t *testing.T) {
	t.Setenv(tracing.ModeEnvVar, string(tracing.ModeOff))
	var exports atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		exports.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)

	r, err := Open(t.TempDir(), "ocel deploy")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	_, span := otel.Tracer("any/package").Start(r.ContextWithRootSpan(context.Background()), "connect")
	recording := span.IsRecording()
	span.End()
	spanOf(r, 1, "building project", progressv1.SpanStatus_SPAN_STATUS_OK)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	if recording {
		t.Error("a span started under the run is recording, want no span recorded with the trace off")
	}
	if got := len(loggedEvents(t, r)); got != 2 {
		t.Errorf("the run's events file holds %d events, want both it received", got)
	}
	for _, name := range []string{TraceFileName, TraceFileName + ".partial"} {
		if _, err := os.Stat(filepath.Join(r.dir, name)); !os.IsNotExist(err) {
			t.Errorf("stat %s = %v, want no trace file with the trace off", name, err)
		}
	}
	if n := exports.Load(); n != 0 {
		t.Errorf("the user's OTLP collector received %d exports, want none with the trace off", n)
	}
}

func TestUnderTraceOffASecretAddedToTheRunIsStillKnownToIt(t *testing.T) {
	t.Setenv(tracing.ModeEnvVar, string(tracing.ModeOff))
	r := startRun(t)
	AddSecretValues(r.ContextWithRootSpan(context.Background()), "pw5c")
	if got := r.secrets.RedactText("pin pw5c"); got != "pin "+tracing.Redacted {
		t.Errorf("the run's secrets hide %q as %q, want the value added to the run hidden", "pin pw5c", got)
	}
}
