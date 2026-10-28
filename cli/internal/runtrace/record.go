package runtrace

import (
	"strings"

	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	"github.com/ocelhq/ocel/pkg/tracing"
)

type Call struct {
	Span         trace.SpanContext
	ExportsSpans bool
}

func (t *Trace) Receive(call Call, event *streamv1.RunEvent) {
	t.Log(event)
	switch {
	case !t.isRecording():
	case !call.Span.IsValid():
		t.ingest(event, call.ExportsSpans)
	case call.Span.TraceID() == t.traceID:
		t.ingest(nestUnderCall(call.Span.SpanID(), event), call.ExportsSpans)
	}
}

func nestUnderCall(call trace.SpanID, event *streamv1.RunEvent) *streamv1.RunEvent {
	event = proto.CloneOf(event)
	op := event.GetOperation()
	if id, ok := spanID(op.GetSpanId()); ok {
		stepID := tracing.StepSpanID(call, id[:])
		op.SpanId = stepID[:]
	}
	if started := op.GetStarted(); started != nil {
		parentID := call
		if id, ok := spanID(started.GetParentSpanId()); ok {
			parentID = tracing.StepSpanID(call, id[:])
		}
		started.ParentSpanId = parentID[:]
	}
	return event
}

func (t *Trace) ingest(event *streamv1.RunEvent, providerExportsSpans bool) {
	switch {
	case event.GetOperation().GetStarted() != nil:
		t.remember(event)
	case event.GetOperation().GetEnded() != nil:
		t.ingestEnded(event, providerExportsSpans)
	}
}

func (t *Trace) Log(event *streamv1.RunEvent) {
	event = persisted(event)
	if t.log != nil {
		t.log.Receive(event)
	}
	raw, err := protojson.Marshal(event)
	if err != nil {
		return
	}
	raw = append(raw, '\n')

	t.eventsMu.Lock()
	defer t.eventsMu.Unlock()
	_, _ = t.eventsFile.Write(raw)
}

func persisted(ev *streamv1.RunEvent) *streamv1.RunEvent {
	ev = proto.CloneOf(ev)
	if waiting := ev.GetWaiting(); waiting != nil {
		waiting.Url, _, _ = strings.Cut(waiting.GetUrl(), "#")
	}
	return ev
}
