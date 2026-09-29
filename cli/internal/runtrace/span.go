package runtrace

import (
	"context"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/progress"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type startedScope struct {
	parentID trace.SpanID
	name     string
	phase    string
}

func (r *Run) remember(ev *streamv1.RunEvent) {
	id, ok := spanID(ev.GetSpanId())
	if !ok {
		return
	}
	parentID, _ := spanID(ev.GetStarted().GetParentSpanId())
	phase := strings.ToLower(strings.TrimPrefix(ev.GetPhase().String(), "PHASE_"))
	name := ev.GetMessage()
	if name == "" {
		name = phase + " phase"
	}
	if ev.GetPhase() == progressv1.Phase_PHASE_UNSPECIFIED {
		phase = ""
	}
	r.scopesMu.Lock()
	defer r.scopesMu.Unlock()
	r.scopes[id] = startedScope{parentID: parentID, name: name, phase: phase}
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
	attrs := spanAttributes(ended.GetAttributes())
	if scope.phase != "" {
		attrs = append([]attribute.KeyValue{Attribute(progress.AttrKeyPhase, scope.phase)}, attrs...)
	}
	r.ingestSpan(id, scope.parentID, scope.name, start, end, ended.GetStatus(), attrs)
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
	_, span := r.tracer.Start(ctx, progress.SanitizeSpanName(name), opts...)
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

func spanAttributes(attrs []*progressv1.SpanAttribute) []attribute.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		key, ok := progress.FindAttrKey(a.GetKey())
		if !ok {
			continue
		}
		out = append(out, Attribute(key, a.GetValue()))
	}
	return out
}
