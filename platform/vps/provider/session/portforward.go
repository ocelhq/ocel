package session

import (
	"context"
	"net"
	"os/exec"
	"sync"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func (s *Session) ForwardPort(ctx context.Context, remote string) (string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, refusal.Refuse(refusal.CodeNotReady, "listen on a loopback port to forward to %s from: %s", remote, err)
	}
	relayCtx, cancel := context.WithCancel(ctx)
	var carried sync.WaitGroup
	accepting := make(chan struct{})
	go func() {
		defer close(accepting)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			carried.Go(func() { s.carry(relayCtx, conn, remote) })
		}
	}()
	go func() {
		<-relayCtx.Done()
		_ = listener.Close()
	}()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = listener.Close()
			<-accepting
			carried.Wait()
		})
	}
	return listener.Addr().String(), stop, nil
}

func (s *Session) carry(ctx context.Context, conn net.Conn, remote string) {
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
	cmd := exec.CommandContext(ctx, "ssh", append(s.args(), "-W", remote, s.dest.Written)...)
	cmd.Stdin, cmd.Stdout = socket, socket
	if err := cmd.Start(); err != nil {
		return
	}
	_ = socket.Close()
	_ = conn.Close()
	_ = cmd.Wait()
}
