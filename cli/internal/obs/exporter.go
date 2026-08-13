package obs

import (
	"context"
	"os"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// allowlistExporter wraps another exporter and strips every span attribute
// that fails the vocabulary in allowlist.go before the inner exporter ever
// sees the span. It sits in front of both the local file exporter and the
// network one, so the policy holds regardless of which one — or how many —
// are attached to the TracerProvider.
type allowlistExporter struct {
	inner sdktrace.SpanExporter
}

func (e allowlistExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	filtered := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		filtered[i] = filteredSpan{s}
	}
	return e.inner.ExportSpans(ctx, filtered)
}

func (e allowlistExporter) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}

type filteredSpan struct {
	sdktrace.ReadOnlySpan
}

func (f filteredSpan) Attributes() []attribute.KeyValue {
	return filterAttributes(f.ReadOnlySpan.Attributes())
}

// fileExporter accumulates every span it is handed and, on each export,
// rewrites a single OTLP/JSON trace file holding the whole run so far. A CLI
// run is short and low-volume enough that a full rewrite per batch is
// cheaper than the bookkeeping an append-only encoding would need, and it
// guarantees the file is always one well-formed TracesData document that an
// OTLP viewer opens with no conversion step.
type fileExporter struct {
	path string

	mu    sync.Mutex
	spans []*tracepb.Span
}

func newFileExporter(path string) *fileExporter {
	return &fileExporter{path: path}
}

func (e *fileExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, s := range spans {
		e.spans = append(e.spans, convertSpan(s))
	}
	return e.flushLocked()
}

func (e *fileExporter) Shutdown(context.Context) error {
	return nil
}

func (e *fileExporter) flushLocked() error {
	data := &tracepb.TracesData{
		ResourceSpans: []*tracepb.ResourceSpans{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{
					stringKV("service.name", "ocel"),
				},
			},
			ScopeSpans: []*tracepb.ScopeSpans{{
				Scope: &commonpb.InstrumentationScope{Name: "github.com/ocelhq/ocel/cli"},
				Spans: e.spans,
			}},
		}},
	}
	raw, err := protojson.Marshal(data)
	if err != nil {
		return err
	}
	return os.WriteFile(e.path, raw, 0o644)
}

func convertSpan(s sdktrace.ReadOnlySpan) *tracepb.Span {
	sc := s.SpanContext()
	traceID := sc.TraceID()
	spanID := sc.SpanID()

	span := &tracepb.Span{
		TraceId:           traceID[:],
		SpanId:            spanID[:],
		Name:              s.Name(),
		Kind:              tracepb.Span_SpanKind(s.SpanKind()),
		StartTimeUnixNano: uint64(s.StartTime().UnixNano()),
		EndTimeUnixNano:   uint64(s.EndTime().UnixNano()),
		Attributes:        convertAttributes(filterAttributes(s.Attributes())),
	}
	if parent := s.Parent(); parent.HasSpanID() {
		parentID := parent.SpanID()
		span.ParentSpanId = parentID[:]
	}
	if status := s.Status(); status.Code != codes.Unset {
		span.Status = &tracepb.Status{
			Message: status.Description,
			Code:    tracepb.Status_StatusCode(status.Code),
		}
	}
	return span
}

func convertAttributes(attrs []attribute.KeyValue) []*commonpb.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]*commonpb.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, &commonpb.KeyValue{
			Key:   string(a.Key),
			Value: convertValue(a.Value),
		})
	}
	return out
}

func convertValue(v attribute.Value) *commonpb.AnyValue {
	switch v.Type() {
	case attribute.BOOL:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: v.AsBool()}}
	case attribute.INT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: v.AsInt64()}}
	case attribute.FLOAT64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: v.AsFloat64()}}
	default:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v.Emit()}}
	}
}

func stringKV(key, value string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key:   key,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: value}},
	}
}
