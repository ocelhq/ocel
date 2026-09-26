package bindingproxy

import (
	"context"
	"errors"
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
)

type tokenGate struct{ token string }

func (a *tokenGate) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := a.check(req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (a *tokenGate) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *tokenGate) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if err := a.check(conn.RequestHeader()); err != nil {
			return err
		}
		return next(ctx, conn)
	}
}

func (a *tokenGate) check(header http.Header) error {
	if !channel.VerifyAuthHeader(header.Get("Authorization"), a.token) {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid session token"))
	}
	return nil
}
