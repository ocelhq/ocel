package runtrace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/ocelhq/ocel/pkg/constants"
)

func TestStartNamesArtifactsByTraceID(t *testing.T) {
	dir := t.TempDir()
	ctx, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	r.Log(ctx, "web", "building project")
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	runsDir := filepath.Join(dir, constants.ProjectStateDirName, "runs")
	wantLog := filepath.Join(runsDir, r.TraceID()+".ndjson")
	wantTrace := filepath.Join(runsDir, r.TraceID()+".otlp.json")

	if _, err := os.Stat(wantLog); err != nil {
		t.Errorf("log file %s not written: %v", wantLog, err)
	}
	if _, err := os.Stat(wantTrace); err != nil {
		t.Errorf("trace file %s not written: %v", wantTrace, err)
	}
	if len(r.TraceID()) != 32 {
		t.Errorf("TraceID() = %q, want a 32-char hex trace id", r.TraceID())
	}
}

func TestOTLPFileHasARootSpanForTheCommand(t *testing.T) {
	dir := t.TempDir()
	ctx, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	_, span := r.StartSpan(ctx, "building")
	span.End()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	raw, err := os.ReadFile(r.LogPath()[:len(r.LogPath())-len(".ndjson")] + ".otlp.json")
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					Name         string `json:"name"`
					TraceId      string `json:"traceId"`
					ParentSpanId string `json:"parentSpanId"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("trace file is not valid OTLP/JSON: %v", err)
	}

	var spans []struct {
		Name         string `json:"name"`
		TraceId      string `json:"traceId"`
		ParentSpanId string `json:"parentSpanId"`
	}
	for _, rs := range doc.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			spans = append(spans, ss.Spans...)
		}
	}
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2 (root + building)", len(spans))
	}
	var root *struct {
		Name         string `json:"name"`
		TraceId      string `json:"traceId"`
		ParentSpanId string `json:"parentSpanId"`
	}
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

func TestAttributesOutsideTheAllowlistNeverReachEitherArtifact(t *testing.T) {
	dir := t.TempDir()
	ctx, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}

	const secret = "sk_live_topsecret12345"
	const varValue = "postgres://user:hunter2@host/db"

	spanCtx, span := r.StartSpan(ctx, "provisioning",
		attribute.String("credential", secret),
		attribute.String("ocel.span_name", "provisioning"),
	)
	span.SetAttributes(attribute.String("env.DATABASE_URL", varValue))
	r.Log(spanCtx, "web", "provisioning resources")

	const statusSecret = "AKIAFAKESECRETACCESSKEY12345"
	span.SetStatus(codes.Error, "connection failed: aws_secret_access_key="+statusSecret)

	const recordErrSecret = "postgres://user:s3cr3t-recorded@host/db"
	span.RecordError(errors.New("dial failed: " + recordErrSecret))

	span.End()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	logRaw, err := os.ReadFile(r.LogPath())
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	traceRaw, err := os.ReadFile(r.LogPath()[:len(r.LogPath())-len(".ndjson")] + ".otlp.json")
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}

	for _, leak := range []string{secret, varValue, "credential", "env.DATABASE_URL", statusSecret, recordErrSecret} {
		if strings.Contains(string(logRaw), leak) {
			t.Errorf("log file contains disallowed value/key %q", leak)
		}
		if strings.Contains(string(traceRaw), leak) {
			t.Errorf("trace file contains disallowed value/key %q", leak)
		}
	}
	if !strings.Contains(string(logRaw), "web") {
		t.Errorf("log file lost the record's app")
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
	Events []struct {
		Name       string           `json:"name"`
		Attributes []map[string]any `json:"attributes"`
	} `json:"events"`
}

func readTraceDoc(t *testing.T, r *Run) []otlpTestSpan {
	t.Helper()
	raw, err := os.ReadFile(r.LogPath()[:len(r.LogPath())-len(".ndjson")] + ".otlp.json")
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
	dir := t.TempDir()
	ctx, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	_, span := r.StartSpan(ctx, "building")
	span.End()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	if len(spans) == 0 {
		t.Fatal("no spans in the trace file")
	}
	for _, s := range spans {
		if s.TraceId != r.TraceID() {
			t.Errorf("span %q traceId = %q, want %q", s.Name, s.TraceId, r.TraceID())
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
	dir := t.TempDir()
	ctx, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}

	_, ok := r.StartSpan(ctx, "ok-span")
	ok.SetStatus(codes.Ok, "")
	ok.End()

	_, failed := r.StartSpan(ctx, "err-span")
	failed.SetStatus(codes.Error, "boom")
	failed.End()

	_, unset := r.StartSpan(ctx, "unset-span")
	unset.End()

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
	dir := t.TempDir()
	ctx, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	_, span := r.StartSpan(ctx, "building")
	span.End()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	building := spanNamed(t, spans, "building")
	if building.Kind != "SPAN_KIND_INTERNAL" {
		t.Errorf("building span kind = %q, want SPAN_KIND_INTERNAL", building.Kind)
	}
}

func TestSpanEventsAreIncludedButFiltered(t *testing.T) {
	dir := t.TempDir()
	ctx, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	_, span := r.StartSpan(ctx, "provisioning")
	const secret = "sk_live_topsecret12345"
	span.RecordError(errors.New("dial failed: " + secret))
	span.End()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	provisioning := spanNamed(t, spans, "provisioning")
	if len(provisioning.Events) == 0 {
		t.Fatal("RecordError produced no event in the trace file, want the exception event included")
	}
	for _, ev := range provisioning.Events {
		if ev.Name != "exception" {
			t.Errorf("event name = %q, want %q", ev.Name, "exception")
		}
		if len(ev.Attributes) != 0 {
			t.Errorf("event attributes = %v, want none: only ocel.* keys are allowlisted and exception.* is not one of them", ev.Attributes)
		}
	}
}

func TestARunsLogAndTraceAreReadableByTheirOwnerAlone(t *testing.T) {
	dir := t.TempDir()
	ctx, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	r.Log(ctx, "web", "building project")
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	for path, want := range map[string]os.FileMode{
		r.Dir():     0o700,
		r.LogPath(): 0o600,
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
	ctx, r, err := Start(context.Background(), t.TempDir(), "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	tracePath := strings.TrimSuffix(r.LogPath(), ".ndjson") + ".otlp.json"
	for range 3 {
		_, span := r.StartSpan(ctx, "building")
		span.End()
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
