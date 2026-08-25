package runtrace

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

type forcedSpanIDKey struct{}

func contextWithSpanID(ctx context.Context, id trace.SpanID) context.Context {
	return context.WithValue(ctx, forcedSpanIDKey{}, id)
}

func forcedSpanID(ctx context.Context) (trace.SpanID, bool) {
	id, ok := ctx.Value(forcedSpanIDKey{}).(trace.SpanID)
	return id, ok
}

type fixedIDGenerator struct {
	traceID trace.TraceID
}

func (g fixedIDGenerator) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	if id, ok := forcedSpanID(ctx); ok {
		return g.traceID, id
	}
	return g.traceID, newSpanID()
}

func (g fixedIDGenerator) NewSpanID(ctx context.Context, _ trace.TraceID) trace.SpanID {
	if id, ok := forcedSpanID(ctx); ok {
		return id
	}
	return newSpanID()
}
