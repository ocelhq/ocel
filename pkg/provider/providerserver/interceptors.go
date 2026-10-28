package providerserver

import (
	"context"
	"net/http"

	connect "connectrpc.com/connect"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing/rpcspan"
)

type callSpanInterceptor struct{}

func (callSpanInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, span := startCallSpan(ctx, req.Spec().Procedure, req.Header())
		resp, err := next(ctx, req)
		rpcspan.End(span, err)
		return resp, err
	}
}

func (callSpanInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (callSpanInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		ctx, span := startCallSpan(ctx, conn.Spec().Procedure, conn.RequestHeader())
		err := next(ctx, conn)
		rpcspan.End(span, err)
		return err
	}
}

type callKey struct{}

func startCallSpan(ctx context.Context, procedure string, header http.Header) (context.Context, trace.Span) {
	ctx = extractTraceParent(ctx, header)
	caller := trace.SpanContextFromContext(ctx)
	if !caller.IsValid() {
		return ctx, trace.SpanFromContext(ctx)
	}
	ctx = context.WithValue(ctx, callKey{}, caller)
	return rpcspan.Start(ctx, trace.SpanKindServer, procedure)
}

func callerOf(ctx context.Context) trace.SpanContext {
	caller, _ := ctx.Value(callKey{}).(trace.SpanContext)
	return caller
}

func extractTraceParent(ctx context.Context, header http.Header) context.Context {
	return propagation.TraceContext{}.Extract(ctx, propagation.HeaderCarrier(header))
}
