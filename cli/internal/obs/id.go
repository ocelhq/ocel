package obs

import (
	"context"
	"crypto/rand"

	"go.opentelemetry.io/otel/trace"
)

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

type fixedIDGenerator struct {
	traceID trace.TraceID
}

func (g fixedIDGenerator) NewIDs(context.Context) (trace.TraceID, trace.SpanID) {
	return g.traceID, newSpanID()
}

func (g fixedIDGenerator) NewSpanID(context.Context, trace.TraceID) trace.SpanID {
	return newSpanID()
}
