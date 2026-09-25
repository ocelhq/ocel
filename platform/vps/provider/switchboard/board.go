package switchboard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const EdgeName = "box"

var edgeHeader = http.CanonicalHeaderKey(edge.HeaderEdge)

const (
	ReadHeaderTimeout = 10 * time.Second
	idleTimeout       = 5 * time.Minute
	dialTimeout       = 10 * time.Second
	dialKeepAlive     = 30 * time.Second
	idleUpstreams     = 64
	upstreamIdle      = 90 * time.Second
	connectorHost     = "connector"
)

var forwardedKept = []string{"X-Forwarded-Host", "X-Forwarded-Proto"}

const HeardHeader = "X-Ocel-Heard"

type Board struct {
	table     atomic.Pointer[Table]
	loading   sync.Mutex
	retiring  sync.Mutex
	draining  map[string]int
	relayed   []netip.Prefix
	ledger    ledger
	connector string
	tcp       *http.Transport
	socket    *http.Transport
	server    *http.Server
}

func New(table *Table, relayed ...netip.Prefix) *Board {
	board := &Board{connector: ConnectorSocket, draining: map[string]int{}, relayed: slices.Clone(relayed)}
	board.table.Store(table)
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: dialKeepAlive}
	board.tcp = upstreamTransport(func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return board.ledger.wired(ctx, conn)
	})
	board.socket = upstreamTransport(func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", board.connector)
	})
	board.server = &http.Server{
		Handler:           board,
		ReadHeaderTimeout: ReadHeaderTimeout,
		IdleTimeout:       idleTimeout,
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			if _, front := conn.(frontConn); front {
				return context.WithValue(ctx, frontKey{}, true)
			}
			return ctx
		},
	}
	return board
}

type frontKey struct{}

type frontConn struct{ net.Conn }

type frontListener struct{ net.Listener }

func (l frontListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return frontConn{conn}, nil
}

func upstreamTransport(dial func(ctx context.Context, network, address string) (net.Conn, error)) *http.Transport {
	return &http.Transport{
		DialContext:         dial,
		DisableCompression:  true,
		MaxIdleConnsPerHost: idleUpstreams,
		IdleConnTimeout:     upstreamIdle,
	}
}

func (b *Board) Serve(listener net.Listener) error {
	err := b.server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (b *Board) ServeFront(listener net.Listener) error { return b.Serve(frontListener{listener}) }

func (b *Board) Shutdown(ctx context.Context) error {
	err := b.server.Shutdown(ctx)
	b.ledger.cutAll()
	return err
}

func (b *Board) Close() error {
	err := b.server.Close()
	b.ledger.cutAll()
	return err
}

func (b *Board) Grace() time.Duration { return b.table.Load().grace }

func (b *Board) Load(path string) error {
	b.loading.Lock()
	defer b.loading.Unlock()
	document, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	table, err := Read(document)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	b.table.Store(table)
	return nil
}

func (b *Board) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	named := func(header http.Header) {
		header.Set(edgeHeader, EdgeName)
		if r.URL.Path == edge.LivenessProbePath {
			header.Set(HeardHeader, b.heard(r))
		}
	}
	forward, ctx, hangUp, ok := b.forwarding(r)
	if !ok {
		named(w.Header())
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer hangUp()
	transport, host := b.tcp, forward.Upstream
	if forward.Upstream == connectorDial {
		transport, host = b.socket, connectorHost
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(out *httputil.ProxyRequest) {
			out.Out.URL.Scheme = "http"
			out.Out.URL.Host = host
			out.Out.Host = out.In.Host
			if forward.Strip != "" {
				out.Out.URL.Path = stripped(out.In.URL.Path, forward.Strip)
				out.Out.URL.RawPath = ""
				if under(out.In.URL.RawPath, forward.Strip) {
					out.Out.URL.RawPath = stripped(out.In.URL.RawPath, forward.Strip)
				}
			}
			b.forwarded(out)
		},
		Transport:     transport,
		FlushInterval: -1,
		ModifyResponse: func(answer *http.Response) error {
			named(answer.Header)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			named(w.Header())
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r.WithContext(ctx))
}

func (b *Board) forwarding(r *http.Request) (Forward, context.Context, func(), bool) {
	for {
		table := b.table.Load()
		forward, ok := table.Forward(r.Host, r.URL.Path)
		if !ok {
			return Forward{}, nil, nil, false
		}
		ctx, hangUp := b.ledger.take(forward.Upstream, r.Context())
		if b.table.Load() == table {
			return forward, ctx, hangUp, true
		}
		hangUp()
	}
}

func (b *Board) cutUnrouted(address string) {
	b.loading.Lock()
	defer b.loading.Unlock()
	if !b.table.Load().routed[address] {
		b.ledger.cut(address)
	}
}

type hearing int

const (
	hearsNothing hearing = iota
	hearsScheme
	hearsClient
)

func (b *Board) hears(r *http.Request) hearing {
	if r.Context().Value(frontKey{}) != nil {
		return hearsClient
	}
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err == nil && slices.ContainsFunc(b.relayed, func(prefix netip.Prefix) bool { return prefix.Contains(peer.Addr().Unmap()) }) {
		return hearsScheme
	}
	return hearsNothing
}

func (b *Board) heard(r *http.Request) string {
	proto := "http"
	if said := r.Header.Get("X-Forwarded-Proto"); said != "" && b.hears(r) != hearsNothing {
		proto = said
	}
	return proto + " " + r.Host
}

func (b *Board) forwarded(out *httputil.ProxyRequest) {
	switch b.hears(out.In) {
	case hearsNothing:
		out.SetXForwarded()
		return
	case hearsScheme:
		out.SetXForwarded()
	case hearsClient:
		out.Out.Header.Set("X-Forwarded-Host", out.In.Host)
		out.Out.Header.Set("X-Forwarded-Proto", "http")
		if prior := out.In.Header.Values("X-Forwarded-For"); len(prior) > 0 {
			out.Out.Header["X-Forwarded-For"] = slices.Clone(prior)
		}
	}
	for _, kept := range forwardedKept {
		if said := out.In.Header.Get(kept); said != "" {
			out.Out.Header.Set(kept, said)
		}
	}
}

func stripped(requested, prefix string) string {
	rest := requested[len(prefix):]
	if !strings.HasPrefix(rest, "/") {
		rest = "/" + rest
	}
	return rest
}
