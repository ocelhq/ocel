package providerkit

import (
	"context"
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
)

func traceInterceptor() connect.Interceptor {
	return &tracecontext{}
}

type tracecontext struct{}

func (t *tracecontext) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(extractTraceParent(ctx, req.Header()), req)
	}
}

func (t *tracecontext) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (t *tracecontext) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(extractTraceParent(ctx, conn.RequestHeader()), conn)
	}
}

func extractTraceParent(ctx context.Context, header http.Header) context.Context {
	return channel.WithTraceParent(ctx, header.Get(channel.TraceParentHeader))
}
