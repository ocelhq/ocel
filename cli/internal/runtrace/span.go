package runtrace

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const maxSpanNameLen = 200

type startedScope struct {
	parentID trace.SpanID
	name     string
}

func (r *Run) remember(ev *streamv1.RunEvent) {
	id, ok := spanID(ev.GetSpanId())
	if !ok {
		return
	}
	parentID, _ := spanID(ev.GetStarted().GetParentSpanId())
	name := ev.GetMessage()
	if name == "" {
		name = strings.ToLower(strings.TrimPrefix(ev.GetPhase().String(), "PHASE_")) + " phase"
	}
	r.scopesMu.Lock()
	defer r.scopesMu.Unlock()
	r.scopes[id] = startedScope{parentID: parentID, name: name}
}

func (r *Run) ingestEnded(ev *streamv1.RunEvent) {
	id, ok := spanID(ev.GetSpanId())
	if !ok {
		return
	}
	r.scopesMu.Lock()
	scope := r.scopes[id]
	delete(r.scopes, id)
	r.scopesMu.Unlock()

	ended := ev.GetEnded()
	end := ev.GetTime().AsTime().UTC()
	start := end
	if ns := ended.GetStartTimeUnixNano(); ns > 0 && !time.Unix(0, ns).After(end) {
		start = time.Unix(0, ns).UTC()
	}
	r.ingestSpan(id, scope.parentID, scope.name, start, end, ended.GetStatus(), spanAttributes(ended.GetAttributes()))
}

func spanID(raw []byte) (trace.SpanID, bool) {
	var id trace.SpanID
	if len(raw) != len(id) {
		return id, false
	}
	copy(id[:], raw)
	return id, id.IsValid()
}

func (r *Run) ingestSpan(id, parentID trace.SpanID, name string, start, end time.Time, status progressv1.SpanStatus, attrs []attribute.KeyValue) {
	ctx := context.Background()
	parent := r.rootSpan.SpanContext()
	if parentID.IsValid() {
		parent = trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    r.traceID,
			SpanID:     parentID,
			TraceFlags: trace.FlagsSampled,
			Remote:     true,
		})
	}
	ctx = trace.ContextWithRemoteSpanContext(ctx, parent)
	ctx = contextWithSpanID(ctx, id)

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

func spanStatusCode(s progressv1.SpanStatus) (codes.Code, bool) {
	switch s {
	case progressv1.SpanStatus_SPAN_STATUS_OK:
		return codes.Ok, true
	case progressv1.SpanStatus_SPAN_STATUS_ERROR:
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

var numericAttributeKeys = map[attribute.Key]struct{}{
	AttrExitCode:      {},
	AttrResourceCount: {},
	AttrBytes:         {},
	AttrRetryCount:    {},
	AttrDurationMS:    {},
}

func spanAttributes(attrs []*progressv1.SpanAttribute) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		key, ok := attributeKey(a.GetKey())
		if !ok {
			continue
		}
		out = append(out, attributeValue(key, a.GetValue()))
	}
	return out
}

func attributeValue(key attribute.Key, value string) attribute.KeyValue {
	if _, numeric := numericAttributeKeys[key]; numeric {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil {
			return key.Int64(n)
		}
	}
	return key.String(value)
}

func attributeKey(k progressv1.AttributeKey) (attribute.Key, bool) {
	switch k {
	case progressv1.AttributeKey_ATTRIBUTE_KEY_COMMAND:
		return AttrCommand, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_STAGE:
		return AttrStage, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_APP:
		return AttrApp, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_PHASE:
		return AttrPhase, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_PROVIDER:
		return AttrProvider, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_EXIT_CODE:
		return AttrExitCode, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_ERROR_KIND:
		return AttrErrorKind, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_COUNT:
		return AttrResourceCount, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_BYTES:
		return AttrBytes, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_RETRY_COUNT:
		return AttrRetryCount, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_DURATION_MS:
		return AttrDurationMS, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_TYPE:
		return AttrResourceType, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_RESOURCE_NAME:
		return AttrResourceName, true
	case progressv1.AttributeKey_ATTRIBUTE_KEY_CACHED:
		return AttrCached, true
	default:
		return "", false
	}
}
