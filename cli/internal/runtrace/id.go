package runtrace

import (
	"context"
	"crypto/rand"

	"go.opentelemetry.io/otel/trace"
)

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

type runIDGenerator struct{}

func (g runIDGenerator) NewIDs(ctx context.Context) (trace.TraceID, trace.SpanID) {
	traceID := newTraceID()
	return traceID, g.NewSpanID(ctx, traceID)
}

func (runIDGenerator) NewSpanID(ctx context.Context, _ trace.TraceID) trace.SpanID {
	if id, ok := forcedSpanID(ctx); ok {
		return id
	}
	return newSpanID()
}
