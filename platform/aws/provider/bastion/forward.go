package bastion

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"sync"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	maxSessions = 32
	chunkBytes  = 1024
)

type endpoint struct {
	binding string
	host    string
	port    int
}

type forwardGroup struct {
	task          *Task
	open          OpenSessionFunc
	reportFailure func(error)
	ctx           context.Context
	cancel        context.CancelFunc
	slots         chan struct{}
	listeners     []*portListener
	failed        sync.Once

	mu        sync.Mutex
	remaining int
	closing   bool
	handlers  sync.WaitGroup
}

type portListener struct {
	group    *forwardGroup
	endpoint endpoint
	listener net.Listener

	mu     sync.Mutex
	ended  bool
	active map[*connection]struct{}
}

type connection struct {
	conn   net.Conn
	stream Stream
	end    func()
}

func (b Bastion) Forward(ctx context.Context, c Clients, open OpenSessionFunc, reportFailure func(error), bindings []provider.Binding) ([]provider.PortForward, error) {
	endpoints, err := b.endpointsOf(bindings)
	if err != nil || len(endpoints) == 0 {
		return nil, err
	}
	task, err := b.Run(ctx, c)
	if err != nil {
		return nil, err
	}
	netListeners := make([]net.Listener, 0, len(endpoints))
	for _, reached := range endpoints {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for _, opened := range netListeners {
				_ = opened.Close()
			}
			return nil, errors.Join(fmt.Errorf("listen on the loopback for %s: %w", reached.binding, err), task.Stop())
		}
		netListeners = append(netListeners, listener)
	}
	groupCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	group := &forwardGroup{task: task, open: open, reportFailure: reportFailure, ctx: groupCtx, cancel: cancel, slots: make(chan struct{}, maxSessions), remaining: len(endpoints)}
	forwards := make([]provider.PortForward, 0, len(endpoints))
	for i, reached := range endpoints {
		listening := &portListener{group: group, endpoint: reached, listener: netListeners[i], active: map[*connection]struct{}{}}
		group.listeners = append(group.listeners, listening)
		forwards = append(forwards, provider.PortForward{Binding: reached.binding, LocalAddress: netListeners[i].Addr().String(), Close: listening.close})
	}
	for _, listening := range group.listeners {
		go listening.accept()
	}
	context.AfterFunc(ctx, group.closeAll)
	return forwards, nil
}

func (b Bastion) endpointsOf(bindings []provider.Binding) ([]endpoint, error) {
	endpoints := make([]endpoint, 0, len(bindings))
	for _, binding := range bindings {
		host, port := binding.Properties[provider.PropertyHost], binding.Properties[provider.PropertyPort]
		number, err := strconv.Atoi(port)
		if err != nil || host == "" {
			return nil, fmt.Errorf("binding %s names host %q and port %q, which no port forward can reach", binding.Name, host, port)
		}
		if !slices.Contains(b.Ports, number) {
			return nil, fmt.Errorf("the bastion's security group lets nothing out on port %d, so %s at %s:%d cannot be reached", number, binding.Name, host, number)
		}
		endpoints = append(endpoints, endpoint{binding: binding.Name, host: host, port: number})
	}
	return endpoints, nil
}

func (g *forwardGroup) closeAll() {
	for _, listening := range g.listeners {
		listening.close()
	}
}

func (g *forwardGroup) fail(err error) {
	g.failed.Do(func() {
		g.reportFailure(err)
		go g.closeAll()
	})
}

func (g *forwardGroup) admit() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closing {
		return false
	}
	g.handlers.Add(1)
	return true
}

func (g *forwardGroup) finish() {
	g.mu.Lock()
	g.closing = true
	g.mu.Unlock()
	g.cancel()
	g.handlers.Wait()
	if err := g.task.Stop(); err != nil {
		g.reportFailure(err)
	}
}

func (l *portListener) close() {
	l.mu.Lock()
	if l.ended {
		l.mu.Unlock()
		return
	}
	l.ended = true
	active := make([]*connection, 0, len(l.active))
	for conn := range l.active {
		active = append(active, conn)
	}
	l.mu.Unlock()

	_ = l.listener.Close()
	for _, conn := range active {
		conn.end()
	}
	l.group.mu.Lock()
	l.group.remaining--
	last := l.group.remaining == 0
	l.group.mu.Unlock()
	if last {
		l.group.finish()
	}
}

func (l *portListener) accept() {
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			return
		}
		select {
		case l.group.slots <- struct{}{}:
		case <-l.group.ctx.Done():
			_ = conn.Close()
			return
		}
		if !l.group.admit() {
			<-l.group.slots
			_ = conn.Close()
			return
		}
		go func() {
			defer l.group.handlers.Done()
			defer func() { <-l.group.slots }()
			l.forwardConnection(conn)
		}()
	}
}

func (l *portListener) forwardConnection(conn net.Conn) {
	stream, err := l.group.open(l.group.ctx, l.group.task.ManagedNode, l.endpoint.host, l.endpoint.port)
	if err != nil {
		_ = conn.Close()
		if l.group.ctx.Err() != nil {
			return
		}
		err = fmt.Errorf("open a session to %s at %s:%d: %w", l.endpoint.binding, l.endpoint.host, l.endpoint.port, err)
		if reason, stopped := l.group.task.stoppedReason(l.group.ctx); stopped {
			err = fmt.Errorf("the bastion task stopped (%s), so every port forward through it has ended: %w", reason, err)
		}
		l.group.fail(err)
		return
	}
	open := &connection{conn: conn, stream: stream}
	open.end = sync.OnceFunc(func() {
		_ = conn.Close()
		_ = stream.Terminate()
	})
	l.mu.Lock()
	if l.ended {
		l.mu.Unlock()
		open.end()
		return
	}
	l.active[open] = struct{}{}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.active, open)
		l.mu.Unlock()
	}()

	received := make(chan struct{})
	go func() {
		defer close(received)
		defer open.end()
		for {
			payload, receiveErr := stream.Receive()
			if len(payload) > 0 {
				if _, writeErr := conn.Write(payload); writeErr != nil {
					return
				}
			}
			if receiveErr != nil {
				return
			}
		}
	}()
	buf := make([]byte, chunkBytes)
	for {
		n, readErr := conn.Read(buf)
		if n > 0 {
			if sendErr := stream.Send(buf[:n]); sendErr != nil {
				break
			}
		}
		if readErr != nil {
			break
		}
	}
	open.end()
	<-received
}
