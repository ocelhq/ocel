package obs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func TestStartNamesArtifactsByTraceID(t *testing.T) {
	dir := t.TempDir()
	ctx, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	r.Info(ctx, "building", "web", "building project")
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	runsDir := filepath.Join(dir, ".ocel", "runs")
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

func TestLogRecordCarriesTheVocabulary(t *testing.T) {
	dir := t.TempDir()
	ctx, r, err := Start(context.Background(), dir, "ocel deploy")
	if err != nil {
		t.Fatalf("Start() = %v", err)
	}
	spanCtx, span := r.StartSpan(ctx, "uploading")
	r.Info(spanCtx, "uploading", "web", "uploading function artifacts")
	span.End()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	lines := readLines(t, r.LogPath())
	if len(lines) != 1 {
		t.Fatalf("got %d log lines, want 1", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	for _, field := range []string{"time", "level", "message", "stage", "app", "trace_id", "span_id"} {
		if _, ok := rec[field]; !ok {
			t.Errorf("record %v missing field %q", rec, field)
		}
	}
	if rec["trace_id"] != r.TraceID() {
		t.Errorf("trace_id = %v, want %v", rec["trace_id"], r.TraceID())
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
		attribute.String("ocel.stage", "provisioning"),
	)
	span.SetAttributes(attribute.String("env.DATABASE_URL", varValue))
	r.Info(spanCtx, "provisioning", "web", "provisioning resources",
		attribute.String("credential", secret),
		attribute.String("env.DATABASE_URL", varValue),
		AttrApp.String("web"),
	)
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

	for _, leak := range []string{secret, varValue, "credential", "env.DATABASE_URL"} {
		if strings.Contains(string(logRaw), leak) {
			t.Errorf("log file contains disallowed value/key %q", leak)
		}
		if strings.Contains(string(traceRaw), leak) {
			t.Errorf("trace file contains disallowed value/key %q", leak)
		}
	}
	if !strings.Contains(string(logRaw), "web") {
		t.Errorf("log file lost the allowlisted app attribute")
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
