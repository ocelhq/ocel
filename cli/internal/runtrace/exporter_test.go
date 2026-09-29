package runtrace

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestFileExporter(t *testing.T) {
	t.Parallel()

	t.Run("an exported span is on disk before shutdown, so a long session holds none in memory", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "run.otlp.json")
		exporter := newFileExporter(path)
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
		exporter := newFileExporter(path)
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

	t.Run("a trace with no span still shuts down to a valid document", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "run.otlp.json")
		if err := newFileExporter(path).Shutdown(context.Background()); err != nil {
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
