package runtrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func exportingTo(t *testing.T, file *traceFile) *otlptrace.Exporter {
	t.Helper()
	exporter, err := otlptrace.New(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	return exporter
}

func TestFileExporter(t *testing.T) {
	t.Parallel()

	t.Run("an exported span is on disk before shutdown, so a long session holds none in memory", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "run.otlp.json")
		file := newTraceFile(path)
		exporter := exportingTo(t, file)
		spans := tracetest.SpanStubs{{Name: "first"}, {Name: "second"}}.Snapshots()
		if err := exporter.ExportSpans(context.Background(), spans); err != nil {
			t.Fatalf("ExportSpans: %v", err)
		}
		if err := exporter.ExportSpans(context.Background(), tracetest.SpanStubs{{Name: "third"}}.Snapshots()); err != nil {
			t.Fatalf("ExportSpans: %v", err)
		}

		written, err := os.ReadFile(path + ".partial")
		if err != nil {
			t.Fatalf("read the partial trace before shutdown: %v", err)
		}
		for _, name := range []string{"first", "second", "third"} {
			if !strings.Contains(string(written), `"`+name+`"`) {
				t.Errorf("partial trace = %s, want span %q already written", written, name)
			}
		}
		if err := exporter.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	})

	t.Run("after shutdown the trace is one OTLP document holding every span, and the partial file is gone", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "run.otlp.json")
		file := newTraceFile(path)
		exporter := exportingTo(t, file)
		for _, name := range []string{"first", "second", "third"} {
			if err := exporter.ExportSpans(context.Background(), tracetest.SpanStubs{{Name: name}}.Snapshots()); err != nil {
				t.Fatalf("ExportSpans: %v", err)
			}
		}
		if err := exporter.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read trace: %v", err)
		}
		var doc otlpTestDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("trace is not valid OTLP/JSON: %v\n%s", err, raw)
		}
		var names []string
		for _, resource := range doc.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for _, span := range scope.Spans {
					names = append(names, span.Name)
				}
			}
		}
		if strings.Join(names, ",") != "first,second,third" {
			t.Errorf("spans = %v, want first, second and third in export order", names)
		}
		if _, err := os.Stat(path + ".partial"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("stat partial trace = %v, want it renamed into place", err)
		}
	})

	t.Run("spans past the file's byte budget are dropped and one marker span counts them", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "run.otlp.json")
		file := newTraceFile(path)
		exporter := exportingTo(t, file)
		file.budgetBytes, file.failureReserveBytes = 600, 0
		traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
		for i := range 20 {
			stub := tracetest.SpanStub{
				Name:        fmt.Sprintf("span-%02d", i),
				SpanContext: trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: trace.SpanID{byte(i + 1)}}),
				Parent:      trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: trace.SpanID{0xff}}),
			}
			if err := exporter.ExportSpans(context.Background(), tracetest.SpanStubs{stub}.Snapshots()); err != nil {
				t.Fatalf("ExportSpans: %v", err)
			}
		}
		if err := exporter.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read trace: %v", err)
		}
		if len(raw) > 2*file.budgetBytes {
			t.Errorf("trace is %d bytes, want it held near its budget of %d", len(raw), file.budgetBytes)
		}
		var doc otlpTestDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("trace is not valid OTLP/JSON: %v\n%s", err, raw)
		}
		var kept int
		var marker *otlpTestSpan
		for _, resource := range doc.ResourceSpans {
			for _, scope := range resource.ScopeSpans {
				for i, span := range scope.Spans {
					if strings.HasPrefix(span.Name, "span-") {
						kept++
					} else {
						marker = &scope.Spans[i]
					}
				}
			}
		}
		if kept == 0 || kept == 20 {
			t.Fatalf("kept %d of 20 spans, want the first ones up to the budget", kept)
		}
		if marker == nil {
			t.Fatal("no marker span records the dropped spans")
		}
		if marker.TraceId != traceID.String() {
			t.Errorf("marker trace = %s, want the dropped spans' %s", marker.TraceId, traceID)
		}
		for _, attr := range marker.Attributes {
			if attr.Key == string(droppedSpanCountKey) && attr.Value["intValue"] != fmt.Sprint(20-kept) {
				t.Errorf("marker counts %v dropped spans, want %d", attr.Value["intValue"], 20-kept)
			}
		}
	})

	t.Run("past its budget the file still holds the run's root span and the spans that failed", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "run.otlp.json")
		file := newTraceFile(path)
		exporter := exportingTo(t, file)
		file.budgetBytes, file.failureReserveBytes = 2000, 1000
		traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
		root := trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: trace.SpanID{0xff}})
		export := func(stub tracetest.SpanStub) {
			if err := exporter.ExportSpans(context.Background(), tracetest.SpanStubs{stub}.Snapshots()); err != nil {
				t.Fatalf("ExportSpans: %v", err)
			}
		}
		for i := range 40 {
			export(tracetest.SpanStub{
				Name:        fmt.Sprintf("GET example.com %02d", i),
				SpanContext: trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: trace.SpanID{byte(i + 1)}}),
				Parent:      root,
			})
		}
		export(tracetest.SpanStub{
			Name:        "the step that failed",
			SpanContext: trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: trace.SpanID{0xfe}}),
			Parent:      root,
			Status:      sdktrace.Status{Code: codes.Error},
		})
		export(tracetest.SpanStub{Name: "ocel deploy", SpanContext: root})
		if err := exporter.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read trace: %v", err)
		}
		for _, want := range []string{`"name":"ocel deploy"`, `"name":"the step that failed"`, "spans dropped"} {
			if !strings.Contains(string(raw), want) {
				t.Errorf("trace past its budget lacks %s:\n%s", want, raw)
			}
		}
	})

	t.Run("a trace with no span still shuts down to a valid document", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "run.otlp.json")
		if err := exportingTo(t, newTraceFile(path)).Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read trace: %v", err)
		}
		var doc otlpTestDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("trace is not valid OTLP/JSON: %v\n%s", err, raw)
		}
	})
}

func TestTheTraceFileKeepsEveryAttributeAndEventWithCredentialsRedacted(t *testing.T) {
	r := startRun(t)
	_, span := otel.Tracer("any/package").Start(r.ContextWithRootSpan(context.Background()), "PUT bucket")
	span.SetAttributes(
		attribute.Int("http.response.status_code", 403),
		attribute.String("http.request.header.authorization", "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20261027"),
		attribute.String("url.full", "https://bucket.s3.amazonaws.com/key?X-Amz-Signature=deadbeef&partNumber=2"),
	)
	span.AddEvent("retry", trace.WithAttributes(attribute.String("reason", "SlowDown")))
	span.End()
	if err := r.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(r.dir, TraceFileName))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, kept := range []string{"http.response.status_code", "403", "partNumber=2", "SlowDown", "http.request.header.authorization"} {
		if !strings.Contains(doc, kept) {
			t.Errorf("trace file lacks %q", kept)
		}
	}
	for _, secret := range []string{"AKIDEXAMPLE", "deadbeef"} {
		if strings.Contains(doc, secret) {
			t.Errorf("trace file holds the credential %q", secret)
		}
	}
}
