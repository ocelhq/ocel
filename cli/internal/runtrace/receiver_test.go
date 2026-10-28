package runtrace

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/tracing"
)

func childEnvValue(env []string, name string) string {
	for _, keyValue := range env {
		if k, v, ok := strings.Cut(keyValue, "="); ok && k == name {
			return v
		}
	}
	return ""
}

func childEnvOf(ctx context.Context) []string {
	collector := tracing.ReadCollector()
	return append(tracing.ParentEnv(ctx), tracing.CollectorURLEnvVar+"="+collector.URL, tracing.CollectorTokenEnvVar+"="+collector.Token)
}

func childExporter(t *testing.T, env []string) sdktrace.SpanExporter {
	t.Helper()
	endpoint := childEnvValue(env, tracing.CollectorURLEnvVar)
	if endpoint == "" {
		t.Fatalf("child env = %v, want the run's collector", env)
	}
	exporter, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(endpoint),
		otlptracehttp.WithHeaders(map[string]string{"Authorization": localrpc.FormatAuthHeader(childEnvValue(env, tracing.CollectorTokenEnvVar))}))
	if err != nil {
		t.Fatal(err)
	}
	return exporter
}

func exportRequest(t *testing.T, r *Trace, name string) []byte {
	t.Helper()
	parent := r.rootSpan.SpanContext()
	traceID, parentID, spanID := parent.TraceID(), parent.SpanID(), newSpanID()
	raw, err := proto.Marshal(&collectortrace.ExportTraceServiceRequest{ResourceSpans: []*tracev1.ResourceSpans{{
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{
			TraceId: traceID[:], SpanId: spanID[:], ParentSpanId: parentID[:], Name: name,
		}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func postToCollector(t *testing.T, header http.Header, body []byte) int {
	t.Helper()
	collector := tracing.ReadCollector()
	req, err := http.NewRequest(http.MethodPost, collector.URL, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header = header
	resp, err := (&http.Client{Transport: &http.Transport{}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func authorized(contentType string) http.Header {
	return http.Header{
		"Content-Type":  {contentType},
		"Authorization": {localrpc.FormatAuthHeader(tracing.ReadCollector().Token)},
	}
}

func TestAnExportWithoutTheRunsTokenIsRefusedAndNotWritten(t *testing.T) {
	r := startRun(t)
	header := authorized("application/x-protobuf")
	header.Set("Authorization", localrpc.FormatAuthHeader("guessed"))
	if got := postToCollector(t, header, exportRequest(t, r, "forged")); got != http.StatusForbidden {
		t.Errorf("status = %d, want %d", got, http.StatusForbidden)
	}
	header.Del("Authorization")
	if got := postToCollector(t, header, exportRequest(t, r, "forged")); got != http.StatusForbidden {
		t.Errorf("status without a token = %d, want %d", got, http.StatusForbidden)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for _, s := range readTraceDoc(t, r) {
		if s.Name == "forged" {
			t.Error("trace holds a span exported without the run's token")
		}
	}
}

func TestAnExportOtherThanOTLPProtobufIsRefusedAsUnsupported(t *testing.T) {
	r := startRun(t)
	t.Cleanup(func() { _ = r.Close() })
	if got := postToCollector(t, authorized("application/json"), []byte(`{"resourceSpans":[]}`)); got != http.StatusUnsupportedMediaType {
		t.Errorf("status = %d, want %d", got, http.StatusUnsupportedMediaType)
	}
}

func TestAnExportWhoseBodyInflatesPastTheLimitIsRefused(t *testing.T) {
	r := startRun(t)
	t.Cleanup(func() { _ = r.Close() })
	var zipped bytes.Buffer
	writer := gzip.NewWriter(&zipped)
	if _, err := writer.Write(make([]byte, maxExportBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	header := authorized("application/x-protobuf")
	header.Set("Content-Encoding", "gzip")
	if got := postToCollector(t, header, zipped.Bytes()); got != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", got, http.StatusRequestEntityTooLarge)
	}
}

func TestASpanAChildProcessExportsToTheEndpointItIsHandedLandsInTheRunsTraceFile(t *testing.T) {
	r := startRun(t)
	env := childEnvOf(r.ContextWithRootSpan(context.Background()))
	exporter := childExporter(t, env)
	child := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
		sdktrace.WithResource(resource.NewSchemaless(attribute.String("service.name", "ocel-provider-aws"))),
	)
	parent := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": childEnvValue(env, "TRACEPARENT")})
	_, span := child.Tracer("child").Start(parent, "dynamodb.GetItem")
	span.SetAttributes(attribute.String("http.request.header.authorization", "AWS4-HMAC-SHA256 Credential=AKIDCHILD"))
	span.End()
	if err := child.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	spans := readTraceDoc(t, r)
	root := spanNamed(t, spans, "ocel deploy")
	got := spanNamed(t, spans, "dynamodb.GetItem")
	if got.ParentSpanId != root.SpanId {
		t.Errorf("child span parent = %q, want the root %q", got.ParentSpanId, root.SpanId)
	}
	raw, err := os.ReadFile(filepath.Join(r.dir, TraceFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ocel-provider-aws") {
		t.Errorf("trace file does not name the child's service")
	}
	if strings.Contains(string(raw), "AKIDCHILD") {
		t.Errorf("trace file holds the child's credential")
	}
}

func TestAChildsArrayValuedCredentialsAreRedactedInTheTraceFile(t *testing.T) {
	r := startRun(t)
	env := childEnvOf(r.ContextWithRootSpan(context.Background()))
	child := sdktrace.NewTracerProvider(sdktrace.WithSyncer(childExporter(t, env)))
	parent := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": childEnvValue(env, "TRACEPARENT")})
	_, span := child.Tracer("child").Start(parent, "GET run.googleapis.com")
	span.SetAttributes(
		attribute.StringSlice("http.request.header.authorization", []string{"Bearer ya29.CHILDACCESSTOKEN"}),
		attribute.StringSlice("http.request.header.x-auth-key", []string{"cfglobalkeyCHILD"}),
		attribute.StringSlice("process.command_args", []string{"tool", "--password", "hunter2CHILD"}),
	)
	span.AddEvent("retry", trace.WithAttributes(attribute.StringSlice("http.request.header.authorization", []string{"Bearer ya29.EVENTTOKEN"})))
	span.End()
	if err := child.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(r.dir, TraceFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "GET run.googleapis.com") {
		t.Fatal("trace file lacks the child's span")
	}
	for _, secret := range []string{"CHILDACCESSTOKEN", "cfglobalkeyCHILD", "hunter2CHILD", "EVENTTOKEN"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("trace file holds the child's %q", secret)
		}
	}
}

func TestASpanFromATraceNoRunHasOpenIsNotWritten(t *testing.T) {
	r := startRun(t)
	exporter := childExporter(t, childEnvOf(r.ContextWithRootSpan(context.Background())))
	stranger := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	_, span := stranger.Tracer("stranger").Start(context.Background(), "injected")
	span.End()
	_ = stranger.Shutdown(context.Background())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	for _, s := range readTraceDoc(t, r) {
		if s.Name == "injected" {
			t.Error("trace holds a span from a trace no run opened")
		}
	}
}

func exportChildSpan(t *testing.T, r *Trace, name string, attrs ...attribute.KeyValue) trace.SpanID {
	t.Helper()
	env := childEnvOf(r.ContextWithRootSpan(context.Background()))
	exporter := childExporter(t, env)
	child := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	parent := propagation.TraceContext{}.Extract(context.Background(), propagation.MapCarrier{"traceparent": childEnvValue(env, "TRACEPARENT")})
	_, span := child.Tracer("child").Start(parent, name, trace.WithAttributes(attrs...))
	span.End()
	if err := child.Shutdown(context.Background()); err != nil {
		t.Error(err)
	}
	return span.SpanContext().SpanID()
}

func TestASpanAChildKeptOpenExportsASecondAfterTheRunEndsLandsInTheTraceFile(t *testing.T) {
	r := startRun(t)
	release := KeepOpen(r.ContextWithRootSpan(context.Background()))
	exported := make(chan struct{})
	go func() {
		defer close(exported)
		time.Sleep(time.Second)
		exportChildSpan(t, r, "pulumi.destroy")
		release()
	}()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	<-exported

	spanNamed(t, readTraceDoc(t, r), "pulumi.destroy")
}

func TestARunWithNoChildKeptOpenClosesWithoutWaiting(t *testing.T) {
	r := startRun(t)
	release := KeepOpen(r.ContextWithRootSpan(context.Background()))
	release()
	start := time.Now()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("Close() took %s with no child left open, want no wait", took)
	}
}

func TestARunWhoseChildNeverFinishesClosesOnceTheFlushWindowRunsOut(t *testing.T) {
	window := childFlushWindow
	childFlushWindow = 100 * time.Millisecond
	t.Cleanup(func() { childFlushWindow = window })
	r := startRun(t)
	_ = KeepOpen(r.ContextWithRootSpan(context.Background()))
	start := time.Now()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if took := time.Since(start); took < childFlushWindow || took > time.Second {
		t.Errorf("Close() took %s, want the flush window of %s and no longer", took, childFlushWindow)
	}
	spanNamed(t, readTraceDoc(t, r), "ocel deploy")
}
