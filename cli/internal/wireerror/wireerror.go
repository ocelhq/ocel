package wireerror

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
)

type Interceptor func(*connect.Error) *clierror.Error

func (code Interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		return resp, code.of(err)
	}
}

func (code Interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		return codedConn{StreamingClientConn: next(ctx, spec), code: code}
	}
}

func (Interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (code Interceptor) of(err error) error {
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return err
	}
	if coded := code(wire); coded != nil {
		return coded
	}
	return err
}

type codedConn struct {
	connect.StreamingClientConn
	code Interceptor
}

func (c codedConn) Send(msg any) error { return c.code.of(c.StreamingClientConn.Send(msg)) }

func (c codedConn) Receive(msg any) error { return c.code.of(c.StreamingClientConn.Receive(msg)) }

func (c codedConn) CloseResponse() error { return c.code.of(c.StreamingClientConn.CloseResponse()) }

type Error struct {
	Message string
	Wire    *connect.Error
}

func (e Error) Error() string { return e.Message }

func (e Error) Unwrap() error { return e.Wire }
