package obs

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
)

// SpanStatus is obs's own status vocabulary for an ingested span. Callers
// must map into it explicitly (a switch, not a numeric cast) — the wire
// enum this is fed from does not share ordinals with either this type or
// go.opentelemetry.io/otel/codes.
type SpanStatus int

const (
	SpanStatusUnset SpanStatus = iota
	SpanStatusOK
	SpanStatusError
)

const maxSpanNameLen = 200

// IngestSpan records a span a provider already finished and shipped back
// over the wire, rather than one this process traced itself — so it
// bypasses the tracer's Start/End lifecycle and its own ID generation,
// using the provider's span and parent ids as given.
//
// The span's name arrived as a free-form string, unlike attributes, which
// are enum-keyed on the wire and so are safe by construction; a provider
// bug or a malicious provider could otherwise write anything into the
// trace artifact under the guise of a span name. It is sanitized here,
// on receipt, rather than trusted.
func (r *Run) IngestSpan(spanID, parentSpanID [8]byte, name string, start, end time.Time, status SpanStatus, attrs []attribute.KeyValue) error {
	span := otlpSpan{
		TraceID:           r.traceID.String(),
		SpanID:            hex.EncodeToString(spanID[:]),
		Name:              sanitizeSpanName(name),
		Kind:              "SPAN_KIND_INTERNAL",
		StartTimeUnixNano: fmt.Sprintf("%d", start.UnixNano()),
		EndTimeUnixNano:   fmt.Sprintf("%d", end.UnixNano()),
		Attributes:        convertAttributes(filterAttributes(attrs)),
	}
	if parentSpanID != ([8]byte{}) {
		span.ParentSpanID = hex.EncodeToString(parentSpanID[:])
	}
	if code := spanStatusCodeJSON(status); code != "" {
		span.Status = &otlpStatus{Code: code}
	}
	return r.fileExp.appendSpan(span)
}

func spanStatusCodeJSON(s SpanStatus) string {
	switch s {
	case SpanStatusOK:
		return "STATUS_CODE_OK"
	case SpanStatusError:
		return "STATUS_CODE_ERROR"
	default:
		return ""
	}
}

// sanitizeSpanName strips control characters and caps the length of an
// untrusted, provider-supplied span name before it reaches the trace
// artifact.
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
