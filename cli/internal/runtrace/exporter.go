package runtrace

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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

	mu      sync.Mutex
	partial *os.File
	written int
	layout  documentLayout
}

type documentLayout struct {
	head, listIndent, tail string
}

func newFileExporter(path string) *fileExporter {
	return &fileExporter{path: path}
}

func (e *fileExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.openPartial(); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, s := range spans {
		raw, err := json.MarshalIndent(convertSpan(s), e.layout.listIndent+"  ", "  ")
		if err != nil {
			return err
		}
		if e.written > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString("\n" + e.layout.listIndent + "  ")
		buf.Write(raw)
		e.written++
	}
	_, err := e.partial.Write(buf.Bytes())
	return err
}

func (e *fileExporter) Shutdown(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.openPartial(); err != nil {
		return err
	}
	tail := e.layout.tail
	if e.written > 0 {
		tail = "\n" + e.layout.listIndent + tail
	}
	_, writeErr := e.partial.WriteString(tail)
	closeErr := e.partial.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(e.partial.Name(), e.path)
}

func (e *fileExporter) openPartial() error {
	if e.partial != nil {
		return nil
	}
	layout, err := newDocumentLayout()
	if err != nil {
		return err
	}
	partial, err := os.OpenFile(e.path+".partial", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := partial.WriteString(layout.head); err != nil {
		_ = partial.Close()
		return err
	}
	e.partial, e.layout = partial, layout
	return nil
}

func newDocumentLayout() (documentLayout, error) {
	empty := otlpTracesData{
		ResourceSpans: []otlpResourceSpans{{
			Resource: otlpResource{
				Attributes: []otlpKeyValue{stringKV("service.name", "ocel")},
			},
			ScopeSpans: []otlpScopeSpans{{
				Scope: otlpScope{Name: "github.com/ocelhq/ocel/cli"},
				Spans: []otlpSpan{},
			}},
		}},
	}
	raw, err := json.MarshalIndent(empty, "", "  ")
	if err != nil {
		return documentLayout{}, err
	}
	head, tail, _ := strings.Cut(string(raw), `"spans": []`)
	lineStart := strings.LastIndexByte(head, '\n') + 1
	return documentLayout{
		head:       head + `"spans": [`,
		listIndent: head[lineStart:],
		tail:       "]" + tail,
	}, nil
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
		s := v.String()
		return otlpAnyValue{StringValue: &s}
	}
}

func stringKV(key, value string) otlpKeyValue {
	return otlpKeyValue{Key: key, Value: otlpAnyValue{StringValue: &value}}
}
