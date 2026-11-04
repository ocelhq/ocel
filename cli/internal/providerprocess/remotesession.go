package providerprocess

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/endpointcall"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type remoteSession struct {
	configure func(ctx context.Context) error

	mu     sync.Mutex
	opened int
}

func (s *remoteSession) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if req.Spec().Procedure == contractv1connect.ProviderServiceConfigureProcedure {
			return next(ctx, req)
		}
		opened := s.openedTimes()
		resp, err := next(ctx, req)
		if !endpointcall.IsSessionEnded(err) {
			return resp, err
		}
		if err := s.reopen(ctx, opened); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (s *remoteSession) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		if spec.StreamType != connect.StreamTypeServer {
			return conn
		}
		return &reopeningConn{StreamingClientConn: conn, ctx: ctx, spec: spec, next: next, session: s, opened: s.openedTimes()}
	}
}

func (*remoteSession) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (s *remoteSession) openedTimes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opened
}

func (s *remoteSession) reopen(ctx context.Context, opened int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opened != opened {
		return nil
	}
	if err := s.configure(ctx); err != nil {
		return fmt.Errorf("configure again the provider session Ocel Cloud ended: %w", err)
	}
	s.opened++
	return nil
}

type reopeningConn struct {
	connect.StreamingClientConn
	ctx     context.Context
	spec    connect.Spec
	next    connect.StreamingClientFunc
	session *remoteSession
	opened  int

	request  any
	received bool
}

func (c *reopeningConn) Send(msg any) error {
	c.request = msg
	return c.StreamingClientConn.Send(msg)
}

func (c *reopeningConn) Receive(msg any) error {
	err := c.StreamingClientConn.Receive(msg)
	if err == nil {
		c.received = true
		return nil
	}
	if c.received || c.request == nil || !endpointcall.IsSessionEnded(err) {
		return err
	}
	if err := c.session.reopen(c.ctx, c.opened); err != nil {
		return err
	}
	_ = c.CloseResponse()
	fresh := c.next(c.ctx, c.spec)
	c.StreamingClientConn, c.received = fresh, true
	if err := fresh.Send(c.request); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if err := fresh.CloseRequest(); err != nil {
		return err
	}
	return fresh.Receive(msg)
}
