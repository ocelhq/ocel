package pulumi

import (
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/tracing"
)

const (
	otelTracesFlag      = "--otel-traces"
	relayHeaderTimeout  = 10 * time.Second
	authorizationHeader = "Authorization"
)

type spanRelay struct {
	endpoint string
	token    string
	server   *http.Server
}

func startSpanRelay(collector tracing.Collector) (*spanRelay, error) {
	forward, err := tracing.NewCollectorProxy(collector)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address, token := listener.Addr().String(), localrpc.NewSessionToken()
	server := &http.Server{Handler: localrpc.LoopbackGuard(address, token, forward), ReadHeaderTimeout: relayHeaderTimeout}
	go func() { _ = server.Serve(listener) }()
	endpoint := url.URL{
		Scheme:   "http",
		Host:     address,
		Path:     tracing.CollectorPath,
		RawQuery: url.Values{authorizationHeader: {localrpc.FormatAuthHeader(token)}}.Encode(),
	}
	return &spanRelay{endpoint: endpoint.String(), token: token, server: server}, nil
}

func (r *spanRelay) Close() error { return r.server.Close() }
