package providerprocess

import (
	"context"
	"errors"
	"io"
	"net/http"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/tracing/rpcspan"
)

type callSpanInterceptor struct{}

type callKey struct{}

func withCallSlot(ctx context.Context) (context.Context, *trace.SpanContext) {
	call := &trace.SpanContext{}
	return context.WithValue(ctx, callKey{}, call), call
}

func startCallSpan(ctx context.Context, procedure string, header http.Header) (context.Context, trace.Span) {
	ctx, span := rpcspan.Start(ctx, trace.SpanKindClient, procedure)
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(header))
	if call, named := ctx.Value(callKey{}).(*trace.SpanContext); named {
		*call = span.SpanContext()
	}
	return ctx, span
}

func (callSpanInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		ctx, span := startCallSpan(ctx, req.Spec().Procedure, req.Header())
		resp, err := next(ctx, req)
		rpcspan.End(span, err)
		return resp, err
	}
}

func (callSpanInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		header := http.Header{}
		ctx, span := startCallSpan(ctx, spec.Procedure, header)
		conn := next(ctx, spec)
		for key, values := range header {
			conn.RequestHeader()[key] = values
		}
		return &spannedStream{StreamingClientConn: conn, span: span}
	}
}

func (callSpanInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

type spannedStream struct {
	connect.StreamingClientConn
	span trace.Span
	err  error
}

func (s *spannedStream) Receive(msg any) error {
	err := s.StreamingClientConn.Receive(msg)
	if err != nil && s.err == nil {
		s.err = err
	}
	return err
}

func (s *spannedStream) CloseResponse() error {
	err := s.StreamingClientConn.CloseResponse()
	if errors.Is(s.err, io.EOF) {
		s.err = nil
	}
	rpcspan.End(s.span, s.err)
	return err
}
