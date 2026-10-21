package cloud

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/version"
)

const (
	VersionHeader = "Ocel-Version"
	SessionHeader = "Ocel-Session"
)

type TokenSource interface {
	Ensure(ctx context.Context) (string, error)
}

type Headers struct {
	Tokens  TokenSource
	Session string
}

func (h Headers) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := h.set(ctx, req.Header()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (h Headers) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if err := h.set(ctx, conn.RequestHeader()); err != nil {
			return unsentConn{StreamingClientConn: conn, err: err}
		}
		return conn
	}
}

func (Headers) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (h Headers) set(ctx context.Context, header http.Header) error {
	token, err := h.Tokens.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("issue a token for Ocel Cloud: %w", err)
	}
	header.Set("Authorization", "Bearer "+token)
	header.Set(VersionHeader, version.Version)
	if h.Session != "" {
		header.Set(SessionHeader, h.Session)
	}
	return nil
}

type unsentConn struct {
	connect.StreamingClientConn
	err error
}

func (c unsentConn) Send(any) error { return c.err }

func (c unsentConn) Receive(any) error { return c.err }

func (c unsentConn) CloseRequest() error { return c.err }

func (c unsentConn) CloseResponse() error { return nil }
