package tracecontext

import (
	"context"
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
)

func Interceptor() connect.Interceptor {
	return &interceptor{}
}

type interceptor struct{}

func (i *interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(extract(ctx, req.Header()), req)
	}
}

func (i *interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(extract(ctx, conn.RequestHeader()), conn)
	}
}

func extract(ctx context.Context, header http.Header) context.Context {
	return channel.WithTraceParent(ctx, header.Get(channel.TraceParentHeader))
}
