package obs

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type SpanStatus int

const (
	SpanStatusUnset SpanStatus = iota
	SpanStatusOK
	SpanStatusError
)

const maxSpanNameLen = 200

func (r *Run) IngestSpan(spanID, parentSpanID [8]byte, name string, start, end time.Time, status SpanStatus, attrs []attribute.KeyValue) {
	ctx := context.Background()
	if parentSpanID != ([8]byte{}) {
		parent := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    r.traceID,
			SpanID:     trace.SpanID(parentSpanID),
			TraceFlags: trace.FlagsSampled,
			Remote:     true,
		})
		ctx = trace.ContextWithRemoteSpanContext(ctx, parent)
	}
	ctx = contextWithSpanID(ctx, trace.SpanID(spanID))

	opts := []trace.SpanStartOption{trace.WithTimestamp(start), trace.WithSpanKind(trace.SpanKindInternal)}
	if len(attrs) > 0 {
		opts = append(opts, trace.WithAttributes(attrs...))
	}
	_, span := r.tracer.Start(ctx, sanitizeSpanName(name), opts...)
	if code, ok := spanStatusCode(status); ok {
		span.SetStatus(code, "")
	}
	span.End(trace.WithTimestamp(end))
}

func spanStatusCode(s SpanStatus) (codes.Code, bool) {
	switch s {
	case SpanStatusOK:
		return codes.Ok, true
	case SpanStatusError:
		return codes.Error, true
	default:
		return codes.Unset, false
	}
}

func sanitizeSpanName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= maxSpanNameLen {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "span"
	}
	return out
}
