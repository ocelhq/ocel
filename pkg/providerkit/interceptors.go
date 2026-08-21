package providerkit

import (
	"context"
	"errors"
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
)

func authInterceptor(token string) connect.Interceptor {
	return &authenticator{token: token}
}

type authenticator struct{ token string }

func (a *authenticator) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := a.check(req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (a *authenticator) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authenticator) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := a.check(conn.RequestHeader()); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (a *authenticator) check(header http.Header) error {
	if !channel.VerifyAuthHeader(header.Get("Authorization"), a.token) {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid session token"))
	}
	return nil
}

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
