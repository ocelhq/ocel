package main

import (
	"context"
	"errors"
	"net/http"

	connect "connectrpc.com/connect"

	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
)

// newAuthInterceptor rejects every call unless its Authorization header
// carries the exact per-session token the CLI passed to this process via
// providerv1.SessionTokenEnvVar at launch.
func newAuthInterceptor(token string) connect.Interceptor {
	return &authInterceptor{token: token}
}

type authInterceptor struct{ token string }

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := a.check(req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := a.check(conn.RequestHeader()); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (a *authInterceptor) check(header http.Header) error {
	got, ok := providerv1.ParseAuthHeader(header.Get("Authorization"))
	if !ok || got != a.token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid session token"))
	}
	return nil
}
