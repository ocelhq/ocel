package runtrace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/redactedspan"
)

type redactingExporter struct {
	inner   sdktrace.SpanExporter
	secrets *tracing.SecretValues
}

func (e redactingExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	redacted := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		redacted[i] = redactedspan.New(s, e.secrets)
	}
	return e.inner.ExportSpans(ctx, redacted)
}

func (e redactingExporter) Shutdown(ctx context.Context) error {
	return e.inner.Shutdown(ctx)
}

type traceFile struct {
	path                string
	budgetBytes         int
	failureReserveBytes int

	mu           sync.Mutex
	partial      *os.File
	entries      int
	writtenBytes int
	dropped      int
	droppedTrace trace.TraceID
	closed       bool
}

type fileEntry struct {
	raw    []byte
	trace  trace.TraceID
	root   bool
	failed bool
}

const (
	documentHead        = `{"resourceSpans":[`
	documentTail        = "]}\n"
	traceFileBytes      = 32 << 20
	failureReserveBytes = 4 << 20
)

func newTraceFile(path string) *traceFile {
	return &traceFile{path: path, budgetBytes: traceFileBytes, failureReserveBytes: failureReserveBytes}
}

func (f *traceFile) Start(context.Context) error { return nil }

func (f *traceFile) UploadTraces(_ context.Context, resourceSpans []*tracev1.ResourceSpans) error {
	var entries []fileEntry
	for _, resource := range resourceSpans {
		for _, scope := range resource.GetScopeSpans() {
			for _, span := range scope.GetSpans() {
				raw, err := marshalOTLPJSON(&tracev1.ResourceSpans{
					Resource:   resource.GetResource(),
					SchemaUrl:  resource.GetSchemaUrl(),
					ScopeSpans: []*tracev1.ScopeSpans{{Scope: scope.GetScope(), SchemaUrl: scope.GetSchemaUrl(), Spans: []*tracev1.Span{span}}},
				})
				if err != nil {
					return err
				}
				entries = append(entries, fileEntry{
					raw:    raw,
					trace:  trace.TraceID(span.GetTraceId()),
					root:   len(span.GetParentSpanId()) == 0,
					failed: span.GetStatus().GetCode() == tracev1.Status_STATUS_CODE_ERROR,
				})
			}
		}
	}
	return f.write(entries)
}

func (f *traceFile) write(entries []fileEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	if err := f.openPartial(); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, entry := range entries {
		if !f.fits(entry) {
			f.dropped++
			f.droppedTrace = entry.trace
			continue
		}
		f.writtenBytes += len(entry.raw)
		f.append(&buf, entry.raw)
	}
	_, err := f.partial.Write(buf.Bytes())
	return err
}

func (f *traceFile) fits(entry fileEntry) bool {
	limitBytes := f.budgetBytes - f.failureReserveBytes
	switch {
	case entry.root:
		return true
	case entry.failed:
		limitBytes = f.budgetBytes
	}
	return f.writtenBytes+len(entry.raw) <= limitBytes
}

func (f *traceFile) append(buf *bytes.Buffer, raw []byte) {
	if f.entries > 0 {
		buf.WriteByte(',')
	}
	buf.WriteByte('\n')
	buf.Write(raw)
	f.entries++
}

func (f *traceFile) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	if err := f.openPartial(); err != nil {
		return err
	}
	var buf bytes.Buffer
	if f.dropped > 0 {
		marker, err := marshalOTLPJSON(droppedSpansMarker(f.droppedTrace, f.dropped, f.budgetBytes))
		if err != nil {
			return err
		}
		f.append(&buf, marker)
	}
	if f.entries > 0 {
		buf.WriteByte('\n')
	}
	buf.WriteString(documentTail)
	_, writeErr := f.partial.Write(buf.Bytes())
	closeErr := f.partial.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(f.partial.Name(), f.path)
}

func droppedSpansMarker(traceID trace.TraceID, dropped, budgetBytes int) *tracev1.ResourceSpans {
	spanID, now := newSpanID(), uint64(time.Now().UnixNano())
	return &tracev1.ResourceSpans{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{stringKeyValue(serviceNameKey, cliServiceName)}},
		ScopeSpans: []*tracev1.ScopeSpans{{
			Scope: &commonv1.InstrumentationScope{Name: instrumentationScope},
			Spans: []*tracev1.Span{{
				TraceId:           traceID[:],
				SpanId:            spanID[:],
				Name:              fmt.Sprintf("%d spans dropped: the trace file reached its %d MiB budget", dropped, budgetBytes>>20),
				Kind:              tracev1.Span_SPAN_KIND_INTERNAL,
				StartTimeUnixNano: now,
				EndTimeUnixNano:   now,
				Attributes: []*commonv1.KeyValue{{
					Key:   string(droppedSpanCountKey),
					Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: int64(dropped)}},
				}},
			}},
		}},
	}
}

func stringKeyValue(key, value string) *commonv1.KeyValue {
	return &commonv1.KeyValue{Key: key, Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}}}
}

func (f *traceFile) openPartial() error {
	if f.partial != nil {
		return nil
	}
	partial, err := os.OpenFile(f.path+".partial", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := partial.WriteString(documentHead); err != nil {
		_ = partial.Close()
		return err
	}
	f.partial = partial
	return nil
}
