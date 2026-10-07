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

type Stream interface {
	Receive() ([]byte, error)
	Send(payload []byte) error
	Terminate() error
}

type OpenSessionFunc func(ctx context.Context, target, host string, port int) (Stream, error)

type Target struct {
	Name string
	Host string
	Port int
}

type Forward struct {
	Name    string
	Address string
	Close   func()
}

type forwarding struct {
	task   *Task
	open   OpenSessionFunc
	report func(error)
	ctx    context.Context
	cancel context.CancelFunc
	slots  chan struct{}

	mu        sync.Mutex
	remaining int
	handlers  sync.WaitGroup
}

type portListener struct {
	group    *forwarding
	target   Target
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

func (b Bastion) ForwardBindings(ctx context.Context, c Clients, open OpenSessionFunc, report func(error), bindings []provider.Binding) ([]provider.PortForward, error) {
	targets := make([]Target, 0, len(bindings))
	for _, binding := range bindings {
		port, err := strconv.Atoi(binding.Properties[provider.PropertyPort])
		if err != nil || binding.Properties[provider.PropertyHost] == "" {
			return nil, fmt.Errorf("binding %s names host %q and port %q, which no port forward can reach", binding.Name, binding.Properties[provider.PropertyHost], binding.Properties[provider.PropertyPort])
		}
		targets = append(targets, Target{Name: binding.Name, Host: binding.Properties[provider.PropertyHost], Port: port})
	}
	forwards, err := b.Forward(ctx, c, open, report, targets)
	if err != nil {
		return nil, err
	}
	forwarded := make([]provider.PortForward, 0, len(forwards))
	for _, forward := range forwards {
		forwarded = append(forwarded, provider.PortForward{Binding: forward.Name, LocalAddress: forward.Address, Close: forward.Close})
	}
	return forwarded, nil
}

func (b Bastion) Forward(ctx context.Context, c Clients, open OpenSessionFunc, report func(error), targets []Target) ([]Forward, error) {
	for _, target := range targets {
		if !slices.Contains(b.Ports, target.Port) {
			return nil, fmt.Errorf("the bastion's security group lets nothing out on port %d, so %s at %s:%d cannot be reached", target.Port, target.Name, target.Host, target.Port)
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	if report == nil {
		report = func(error) {}
	}
	task, err := b.Run(ctx, c)
	if err != nil {
		return nil, err
	}
	netListeners := make([]net.Listener, 0, len(targets))
	for _, target := range targets {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			for _, opened := range netListeners {
				_ = opened.Close()
			}
			return nil, errors.Join(fmt.Errorf("listen on the loopback for %s: %w", target.Name, err), task.Stop())
		}
		netListeners = append(netListeners, listener)
	}
	groupCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	group := &forwarding{task: task, open: open, report: report, ctx: groupCtx, cancel: cancel, slots: make(chan struct{}, maxSessions), remaining: len(targets)}
	listeners := make([]*portListener, 0, len(targets))
	forwards := make([]Forward, 0, len(targets))
	for i, target := range targets {
		forwarded := &portListener{group: group, target: target, listener: netListeners[i], active: map[*connection]struct{}{}}
		listeners = append(listeners, forwarded)
		forwards = append(forwards, Forward{Name: target.Name, Address: netListeners[i].Addr().String(), Close: forwarded.close})
		go forwarded.accept()
	}
	context.AfterFunc(ctx, func() {
		for _, forwarded := range listeners {
			forwarded.close()
		}
	})
	return forwards, nil
}

func (g *forwarding) finish() {
	g.cancel()
	g.handlers.Wait()
	if err := g.task.Stop(); err != nil {
		g.report(err)
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
		l.group.handlers.Add(1)
		go func() {
			defer l.group.handlers.Done()
			defer func() { <-l.group.slots }()
			l.carry(conn)
		}()
	}
}

func (l *portListener) carry(conn net.Conn) {
	stream, err := l.group.open(l.group.ctx, l.group.task.Target, l.target.Host, l.target.Port)
	if err != nil {
		l.group.report(fmt.Errorf("open a session to %s at %s:%d: %w", l.target.Name, l.target.Host, l.target.Port, err))
		_ = conn.Close()
		return
	}
	carried := &connection{conn: conn, stream: stream}
	carried.end = sync.OnceFunc(func() {
		_ = conn.Close()
		_ = stream.Terminate()
	})
	l.mu.Lock()
	if l.ended {
		l.mu.Unlock()
		carried.end()
		return
	}
	l.active[carried] = struct{}{}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.active, carried)
		l.mu.Unlock()
	}()

	received := make(chan struct{})
	go func() {
		defer close(received)
		defer carried.end()
		for {
			payload, err := stream.Receive()
			if len(payload) > 0 {
				if _, werr := conn.Write(payload); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	buf := make([]byte, chunkBytes)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if serr := stream.Send(buf[:n]); serr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	carried.end()
	<-received
}
