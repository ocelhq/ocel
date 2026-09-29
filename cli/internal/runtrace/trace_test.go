package runtrace

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/statedir"
)

func spanOf(r *Trace, id byte, name string, status progressv1.SpanStatus) {
	spanID := []byte{id, 0, 0, 0, 0, 0, 0, 1}
	now := time.Now()
	r.Receive(started(spanID, nil, name))
	r.Receive(ended(spanID, now, now.Add(time.Second), status))
}

func TestOpenNamesArtifactsByTraceID(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	spanOf(r, 1, "building project", progressv1.SpanStatus_SPAN_STATUS_OK)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	runsDir := filepath.Join(dir, statedir.Name, "runs")
	id := r.traceID.String()
	for _, want := range []string{filepath.Join(runsDir, id+".ndjson"), filepath.Join(runsDir, id+".otlp.json")} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("%s not written: %v", want, err)
		}
	}
	if len(id) != 32 {
		t.Errorf("trace id = %q, want a 32-char hex trace id", id)
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
	r.Receive(started(id, nil, "provisioning"))
	failed := ended(id, now, now.Add(time.Second), progressv1.SpanStatus_SPAN_STATUS_ERROR)
	failed.Message = "dial " + secret + " failed"
	r.Receive(failed)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	logRaw, err := os.ReadFile(r.LogPath())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	traceRaw, err := os.ReadFile(strings.TrimSuffix(r.LogPath(), ".ndjson") + ".otlp.json")
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
	raw, err := os.ReadFile(strings.TrimSuffix(r.LogPath(), ".ndjson") + ".otlp.json")
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
	if unsetSpan.Status != nil {
		t.Errorf("unset-span status = %+v, want no status object", unsetSpan.Status)
	}
}

func TestSpanKindIsMappedExplicitly(t *testing.T) {
	r := startRun(t)
	spanOf(r, 1, "building", progressv1.SpanStatus_SPAN_STATUS_OK)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	building := spanNamed(t, readTraceDoc(t, r), "building")
	if building.Kind != "SPAN_KIND_INTERNAL" {
		t.Errorf("building span kind = %q, want SPAN_KIND_INTERNAL", building.Kind)
	}
}

func TestARunsLogAndTraceAreReadableByTheirOwnerAlone(t *testing.T) {
	r := startRun(t)
	spanOf(r, 1, "building project", progressv1.SpanStatus_SPAN_STATUS_OK)
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	for path, want := range map[string]os.FileMode{
		filepath.Dir(r.LogPath()): 0o700,
		r.LogPath():               0o600,
		strings.TrimSuffix(r.LogPath(), ".ndjson") + ".otlp.json": 0o600,
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
	tracePath := strings.TrimSuffix(r.LogPath(), ".ndjson") + ".otlp.json"
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
