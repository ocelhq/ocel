package pulumi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	provider "github.com/pulumi/pulumi-go-provider"
	rpc "github.com/pulumi/pulumi/sdk/v3/proto/go"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

const debugProvidersEnvVar = "PULUMI_DEBUG_PROVIDERS"

type Plugin struct {
	Package string

	Version string

	Provider provider.Provider
}

type hostedPlugins struct {
	declared []Plugin
	once     sync.Once
	setting  string
	err      error
}

func (h *hostedPlugins) attach() (string, error) {
	if h == nil || len(h.declared) == 0 {
		return "", nil
	}
	h.once.Do(func() {
		attached := make([]string, 0, len(h.declared))
		for _, plugin := range h.declared {
			port, err := host(plugin)
			if err != nil {
				h.err = err
				return
			}
			attached = append(attached, fmt.Sprintf("%s:%d", plugin.Package, port))
		}
		h.setting = strings.Join(attached, ",")
	})
	return h.setting, h.err
}

func host(plugin Plugin) (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("listen for the engine's calls into the %s plugin: %w", plugin.Package, err)
	}
	go serveEngines(plugin, listener)
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func serveEngines(plugin Plugin, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			time.Sleep(acceptRetryDelay)
			continue
		}
		go serveEngine(plugin, conn)
	}
}

func serveEngine(plugin Plugin, conn net.Conn) {
	server, err := provider.RawServer(plugin.Package, plugin.Version, plugin.Provider)(nil)
	if err != nil {
		_ = conn.Close()
		return
	}
	serving := grpc.NewServer()
	rpc.RegisterResourceProviderServer(serving, &cancelAfterConfigure{ResourceProviderServer: server})
	_ = serving.Serve(newEngineListener(conn))
	serving.Stop()
}

// HACK: pulumi-go-provider (v1.8.0 and main) writes its run info in Configure unlocked while
// Cancel reads it, and the engine sends Cancel whenever it likes.
type cancelAfterConfigure struct {
	rpc.ResourceProviderServer
	configuring sync.RWMutex
}

func (s *cancelAfterConfigure) Configure(ctx context.Context, req *rpc.ConfigureRequest) (*rpc.ConfigureResponse, error) {
	s.configuring.Lock()
	defer s.configuring.Unlock()
	return s.ResourceProviderServer.Configure(ctx, req)
}

func (s *cancelAfterConfigure) Cancel(ctx context.Context, req *emptypb.Empty) (*emptypb.Empty, error) {
	s.configuring.RLock()
	defer s.configuring.RUnlock()
	return s.ResourceProviderServer.Cancel(ctx, req)
}

const acceptRetryDelay = 10 * time.Millisecond

type engineListener struct {
	conn     *engineConn
	accepted atomic.Bool
}

func newEngineListener(conn net.Conn) *engineListener {
	return &engineListener{conn: &engineConn{Conn: conn, closed: make(chan struct{})}}
}

func (l *engineListener) Accept() (net.Conn, error) {
	if l.accepted.CompareAndSwap(false, true) {
		return l.conn, nil
	}
	<-l.conn.closed
	return nil, net.ErrClosed
}

func (l *engineListener) Close() error { return l.conn.Close() }

func (l *engineListener) Addr() net.Addr { return l.conn.LocalAddr() }

type engineConn struct {
	net.Conn
	closing sync.Once
	closed  chan struct{}
}

func (c *engineConn) Close() error {
	c.closing.Do(func() { close(c.closed) })
	return c.Conn.Close()
}
