package obs

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

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

type fileExporter struct {
	path string

	mu    sync.Mutex
	spans []otlpSpan
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
	doc := otlpTracesData{
		ResourceSpans: []otlpResourceSpans{{
			Resource: otlpResource{
				Attributes: []otlpKeyValue{stringKV("service.name", "ocel")},
			},
			ScopeSpans: []otlpScopeSpans{{
				Scope: otlpScope{Name: "github.com/ocelhq/ocel/cli"},
				Spans: e.spans,
			}},
		}},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}

	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, e.path)
}

type otlpTracesData struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name string `json:"name"`
}

type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	ParentSpanID      string         `json:"parentSpanId,omitempty"`
	Name              string         `json:"name"`
	Kind              string         `json:"kind"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
	Events            []otlpEvent    `json:"events,omitempty"`
	Status            *otlpStatus    `json:"status,omitempty"`
}

type otlpEvent struct {
	TimeUnixNano string         `json:"timeUnixNano"`
	Name         string         `json:"name"`
	Attributes   []otlpKeyValue `json:"attributes,omitempty"`
}

type otlpStatus struct {
	Code string `json:"code"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
}

func convertSpan(s sdktrace.ReadOnlySpan) otlpSpan {
	sc := s.SpanContext()
	traceID := sc.TraceID()
	spanID := sc.SpanID()

	span := otlpSpan{
		TraceID:           hex.EncodeToString(traceID[:]),
		SpanID:            hex.EncodeToString(spanID[:]),
		Name:              s.Name(),
		Kind:              spanKindJSON(s.SpanKind()),
		StartTimeUnixNano: fmt.Sprintf("%d", s.StartTime().UnixNano()),
		EndTimeUnixNano:   fmt.Sprintf("%d", s.EndTime().UnixNano()),
		Attributes:        convertAttributes(filterAttributes(s.Attributes())),
		Events:            convertEvents(s.Events()),
	}
	if parent := s.Parent(); parent.HasSpanID() {
		parentID := parent.SpanID()
		span.ParentSpanID = hex.EncodeToString(parentID[:])
	}
	if status := s.Status(); status.Code != codes.Unset {
		span.Status = &otlpStatus{Code: statusCodeJSON(status.Code)}
	}
	return span
}

func spanKindJSON(k trace.SpanKind) string {
	switch k {
	case trace.SpanKindInternal:
		return "SPAN_KIND_INTERNAL"
	case trace.SpanKindServer:
		return "SPAN_KIND_SERVER"
	case trace.SpanKindClient:
		return "SPAN_KIND_CLIENT"
	case trace.SpanKindProducer:
		return "SPAN_KIND_PRODUCER"
	case trace.SpanKindConsumer:
		return "SPAN_KIND_CONSUMER"
	default:
		return "SPAN_KIND_UNSPECIFIED"
	}
}

func statusCodeJSON(c codes.Code) string {
	switch c {
	case codes.Ok:
		return "STATUS_CODE_OK"
	case codes.Error:
		return "STATUS_CODE_ERROR"
	default:
		return "STATUS_CODE_UNSET"
	}
}

func convertEvents(events []sdktrace.Event) []otlpEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]otlpEvent, 0, len(events))
	for _, ev := range events {
		out = append(out, otlpEvent{
			TimeUnixNano: fmt.Sprintf("%d", ev.Time.UnixNano()),
			Name:         ev.Name,
			Attributes:   convertAttributes(filterAttributes(ev.Attributes)),
		})
	}
	return out
}

func convertAttributes(attrs []attribute.KeyValue) []otlpKeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]otlpKeyValue, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, otlpKeyValue{Key: string(a.Key), Value: convertValue(a.Value)})
	}
	return out
}

func convertValue(v attribute.Value) otlpAnyValue {
	switch v.Type() {
	case attribute.BOOL:
		b := v.AsBool()
		return otlpAnyValue{BoolValue: &b}
	case attribute.INT64:
		s := fmt.Sprintf("%d", v.AsInt64())
		return otlpAnyValue{IntValue: &s}
	case attribute.FLOAT64:
		f := v.AsFloat64()
		return otlpAnyValue{DoubleValue: &f}
	default:
		s := v.Emit()
		return otlpAnyValue{StringValue: &s}
	}
}

func stringKV(key, value string) otlpKeyValue {
	return otlpKeyValue{Key: key, Value: otlpAnyValue{StringValue: &value}}
}
