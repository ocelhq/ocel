package switchboard

import (
	"bufio"
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
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 5 * time.Minute
	dialTimeout       = 10 * time.Second
	dialKeepAlive     = 30 * time.Second
	idleUpstreams     = 64
	upstreamIdle      = 90 * time.Second
	connectorHost     = "connector"
)

var forwardedKept = []string{"X-Forwarded-Host", "X-Forwarded-Proto"}

type Board struct {
	table     atomic.Pointer[Table]
	loading   sync.Mutex
	retiring  sync.Mutex
	draining  map[string]int
	trusted   []netip.Prefix
	ledger    ledger
	connector string
	tcp       *http.Transport
	socket    *http.Transport
	server    *http.Server
}

func New(table *Table, trusted []netip.Prefix) *Board {
	board := &Board{trusted: slices.Clone(trusted), connector: ConnectorSocket, draining: map[string]int{}}
	board.table.Store(table)
	dialer := &net.Dialer{Timeout: dialTimeout, KeepAlive: dialKeepAlive}
	board.tcp = upstreams(dialer.DialContext)
	board.socket = upstreams(func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", board.connector)
	})
	board.server = &http.Server{
		Handler:           board,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
	return board
}

func upstreams(dial func(ctx context.Context, network, address string) (net.Conn, error)) *http.Transport {
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
	forward, ok := b.forwarding(r)
	if !ok {
		w.Header().Set(edgeHeader, EdgeName)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	held := &line{ResponseWriter: w, ledger: &b.ledger, address: forward.Upstream}
	defer held.hangUp()
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
			answer.Header.Set(edgeHeader, EdgeName)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			w.Header().Set(edgeHeader, EdgeName)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(held, r)
}

func (b *Board) forwarding(r *http.Request) (Forward, bool) {
	for {
		table := b.table.Load()
		forward, ok := table.Forward(r.Host, r.URL.Path)
		if !ok {
			return Forward{}, false
		}
		b.ledger.take(forward.Upstream)
		if b.table.Load() == table {
			return forward, true
		}
		b.ledger.drop(forward.Upstream)
	}
}

func (b *Board) forwarded(out *httputil.ProxyRequest) {
	peer, err := netip.ParseAddrPort(out.In.RemoteAddr)
	trusted := err == nil && slices.ContainsFunc(b.trusted, func(prefix netip.Prefix) bool {
		return prefix.Contains(peer.Addr().Unmap())
	})
	if trusted {
		if prior := out.In.Header.Values("X-Forwarded-For"); len(prior) > 0 {
			out.Out.Header["X-Forwarded-For"] = slices.Clone(prior)
		}
	}
	out.SetXForwarded()
	if !trusted {
		return
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

type line struct {
	http.ResponseWriter
	ledger  *ledger
	address string
	conn    net.Conn
}

func (l *line) FlushError() error { return http.NewResponseController(l.ResponseWriter).Flush() }

func (l *line) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffered, err := http.NewResponseController(l.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	l.conn = conn
	l.ledger.hijack(l.address, conn)
	return conn, buffered, nil
}

func (l *line) Unwrap() http.ResponseWriter { return l.ResponseWriter }

func (l *line) hangUp() {
	if l.conn != nil {
		l.ledger.release(l.address, l.conn)
	}
	l.ledger.drop(l.address)
}
