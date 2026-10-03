package gateway

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/ocelhq/ocel/platform/realtime/gatewayenv"
)

const maxSubscriptions = 200

const (
	Subprotocol = "aws-appsync-event-ws"
	SocketPath  = "/event/realtime"
	PublishPath = gatewayenv.PublishPath

	MaxEventBytes = 240 * 1024

	DefaultKeepAlive        = 60 * time.Second
	DefaultQueueBudgetBytes = 1 << 20
)

type Config struct {
	Host             string
	Keys             func(namespace string) (ed25519.PublicKey, bool)
	AllowedOrigins   func() []string
	KeepAlive        time.Duration
	QueueBudgetBytes int
	Now              func() time.Time
}

type Gateway struct {
	cfg Config
	hub *hub
	mux *http.ServeMux

	shutdown    context.Context
	stop        context.CancelFunc
	mu          sync.Mutex
	isClosed    bool
	openSockets sync.WaitGroup
}

func New(cfg Config) *Gateway {
	cfg.KeepAlive = cmp.Or(cfg.KeepAlive, DefaultKeepAlive)
	cfg.QueueBudgetBytes = cmp.Or(cfg.QueueBudgetBytes, DefaultQueueBudgetBytes)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.AllowedOrigins == nil {
		cfg.AllowedOrigins = func() []string { return nil }
	}
	g := &Gateway{cfg: cfg, hub: newHub(), mux: http.NewServeMux()}
	g.shutdown, g.stop = context.WithCancel(context.Background())
	g.mux.HandleFunc("GET "+SocketPath, g.serveSocket)
	g.mux.HandleFunc("POST "+PublishPath, g.servePublish)
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

func (g *Gateway) trackSocket() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.isClosed {
		return false
	}
	g.openSockets.Add(1)
	return true
}

func (g *Gateway) isAllowedOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin == "" || slices.Contains(g.cfg.AllowedOrigins(), origin)
}
