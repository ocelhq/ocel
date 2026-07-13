package runtime

import (
	"context"
	"errors"
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/proto/runtime/v1/runtimev1connect"
)

// NewMux serves RuntimeService behind the same per-session token handshake the
// provider uses: every call must carry Authorization: Bearer <token> matching
// the token the launcher (the membrane, later) passed at startup.
func NewMux(token string, svc runtimev1connect.RuntimeServiceHandler) *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := runtimev1connect.NewRuntimeServiceHandler(
		svc,
		connect.WithInterceptors(newAuthInterceptor(token)),
	)
	mux.Handle(path, handler)
	return mux
}

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
	got, ok := channel.ParseAuthHeader(header.Get("Authorization"))
	if !ok || got != a.token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("missing or invalid session token"))
	}
	return nil
}
