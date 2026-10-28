package ssh

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

func (s *Session) ForwardPort(ctx context.Context, remote string) (string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, refusal.Refuse(refusal.CodeNotReady, "listen on a loopback port to forward to %s from: %s", remote, err)
	}
	local := listener.Addr().String()
	relayCtx, cancel := context.WithCancel(ctx)
	relayCtx, span := otel.Tracer(scope).Start(relayCtx, "forward ssh port", trace.WithAttributes(
		attribute.String("ssh.forward.local", local),
		attribute.String("ssh.forward.remote", remote),
		attribute.String("ssh.principal", s.dest.Principal()),
	))
	var (
		connections sync.WaitGroup
		accepted    atomic.Int64
	)
	accepting := make(chan struct{})
	go func() {
		defer close(accepting)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			connections.Go(func() { s.forwardConnection(relayCtx, conn, remote) })
		}
	}()
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		<-relayCtx.Done()
		_ = listener.Close()
		<-accepting
		connections.Wait()
		span.SetAttributes(attribute.Int64("ssh.forward.connections", accepted.Load()))
		span.End()
	}()
	stop := func() {
		cancel()
		<-ended
	}
	return local, stop, nil
}

func (s *Session) forwardConnection(ctx context.Context, conn net.Conn, remote string) {
	defer conn.Close()
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	socket, err := tcp.File()
	if err != nil {
		return
	}
	defer socket.Close()
	cmd := tracing.CommandContext(ctx, "ssh", append(s.args(), "-W", remote, s.dest.Written)...)
	cmd.Stdin, cmd.Stdout = socket, socket
	if err := cmd.Start(); err != nil {
		return
	}
	_ = socket.Close()
	_ = conn.Close()
	_ = cmd.Wait()
}
