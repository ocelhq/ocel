package obs

import (
	"context"
	"crypto/rand"

	"go.opentelemetry.io/otel/trace"
)

// NewTraceID returns a fresh, random OTel-format trace ID as its lowercase
// hex string. Every real ocel command calls this once and uses the result as
// its run ID: the same string names the NDJSON log, the OTLP trace file, and
// (once other tickets wire it through) whatever CI attaches as a build
// artifact. One identity, no mapping table.
func NewTraceID() string {
	return newTraceID().String()
}

func newTraceID() trace.TraceID {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return trace.TraceID(b)
}

func newSpanID() trace.SpanID {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return trace.SpanID(b)
}

// fixedIDGenerator pins the trace ID a TracerProvider hands to root spans.
// The run needs its trace ID before the first span starts — that ID is what
// names the artifact files — so the generator produces it up front instead
// of letting the SDK pick one when the root span is created.
type fixedIDGenerator struct {
	traceID trace.TraceID
}

func (g fixedIDGenerator) NewIDs(context.Context) (trace.TraceID, trace.SpanID) {
	return g.traceID, newSpanID()
}

func (g fixedIDGenerator) NewSpanID(context.Context, trace.TraceID) trace.SpanID {
	return newSpanID()
}
