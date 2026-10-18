package gateway

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
	"github.com/ocelhq/ocel/platform/realtime/token"
)

const (
	maxSubscriptions   = 200
	maxSocketsPerToken = 1
)

const (
	Subprotocol = "aws-appsync-event-ws"

	MaxEventBytes = 240 * 1024

	DefaultKeepAlive        = 60 * time.Second
	DefaultQueueBudgetBytes = 1 << 20
	DefaultMaxSockets       = 1000
	DefaultPingInterval     = 30 * time.Second
	DefaultPongTimeout      = 10 * time.Second
)

type Config struct {
	Host             string
	Keys             token.Keys
	AllowedOrigins   func() []string
	AllowsAnyOrigin  bool
	MaxSockets       int
	KeepAlive        time.Duration
	QueueBudgetBytes int
	PingInterval     time.Duration
	PongTimeout      time.Duration
	Clock            Clock
	Bus              Bus
}

type Gateway struct {
	cfg Config
	hub *hub
	mux *http.ServeMux

	stopListening func()
	shutdown      context.Context
	stop          context.CancelFunc
	mu            sync.Mutex
	isClosed      bool
	socketCount   int
	tokenSockets  map[string]int
	openSockets   sync.WaitGroup
}

func New(cfg Config) *Gateway {
	cfg.KeepAlive = cmp.Or(cfg.KeepAlive, DefaultKeepAlive)
	cfg.QueueBudgetBytes = cmp.Or(cfg.QueueBudgetBytes, DefaultQueueBudgetBytes)
	cfg.MaxSockets = cmp.Or(cfg.MaxSockets, DefaultMaxSockets)
	cfg.PingInterval = cmp.Or(cfg.PingInterval, DefaultPingInterval)
	cfg.PongTimeout = cmp.Or(cfg.PongTimeout, DefaultPongTimeout)
	if cfg.Clock == nil {
		cfg.Clock = systemClock{}
	}
	if cfg.AllowedOrigins == nil {
		cfg.AllowedOrigins = func() []string { return nil }
	}
	if cfg.Bus == nil {
		cfg.Bus = NewMemoryBus()
	}
	g := &Gateway{cfg: cfg, hub: newHub(), mux: http.NewServeMux(), tokenSockets: map[string]int{}}
	g.stopListening = cfg.Bus.Listen(g.hub.Publish)
	g.shutdown, g.stop = context.WithCancel(context.Background())
	g.mux.HandleFunc("GET "+gatewayenv.SocketPath, g.serveSocket)
	g.mux.HandleFunc("POST "+gatewayenv.PublishPath, g.servePublish)
	return g
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.isAllowedOrigin(r) {
		http.Error(w, "this origin may not reach the realtime gateway", http.StatusForbidden)
		return
	}
	g.mux.ServeHTTP(w, r)
}

func (g *Gateway) Close(ctx context.Context) error {
	g.mu.Lock()
	g.isClosed = true
	g.mu.Unlock()
	g.stopListening()
	g.stop()

	closed := make(chan struct{})
	go func() {
		g.openSockets.Wait()
		close(closed)
	}()
	select {
	case <-closed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Gateway) trackSocket() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.isClosed {
		return errors.New("the realtime gateway is shutting down")
	}
	if g.socketCount >= g.cfg.MaxSockets {
		return fmt.Errorf("the realtime gateway holds its limit of %d sockets", g.cfg.MaxSockets)
	}
	g.socketCount++
	g.openSockets.Add(1)
	return nil
}

func (g *Gateway) releaseSocket() {
	g.mu.Lock()
	g.socketCount--
	g.mu.Unlock()
	g.openSockets.Done()
}

func (g *Gateway) trackToken(connectToken string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.tokenSockets[connectToken] >= maxSocketsPerToken {
		return fmt.Errorf("the connect token already holds its limit of %d socket; ask the app's realtime handler for another", maxSocketsPerToken)
	}
	g.tokenSockets[connectToken]++
	return nil
}

func (g *Gateway) releaseToken(connectToken string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.tokenSockets[connectToken]--
	if g.tokenSockets[connectToken] == 0 {
		delete(g.tokenSockets, connectToken)
	}
}

func (g *Gateway) isAllowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || g.cfg.AllowsAnyOrigin || slices.Contains(g.cfg.AllowedOrigins(), origin)
}
