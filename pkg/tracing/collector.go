package tracing

import (
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"

	"github.com/ocelhq/ocel/pkg/localrpc"
)

const (
	CollectorURLEnvVar   = "OCEL_TRACE_COLLECTOR_URL"
	CollectorTokenEnvVar = "OCEL_TRACE_COLLECTOR_TOKEN"
	CollectorPath        = "/v1/traces"
	CollectorMediaType   = "application/x-protobuf"
)

type Collector struct {
	URL   string
	Token string
}

var (
	collectorMu sync.RWMutex
	collector   Collector
)

func SetCollector(c Collector) {
	collectorMu.Lock()
	defer collectorMu.Unlock()
	collector = c
}

func ReadCollector() Collector {
	collectorMu.RLock()
	defer collectorMu.RUnlock()
	return collector
}

func NewCollectorProxy(c Collector) (http.Handler, error) {
	target, err := url.Parse(c.URL)
	if err != nil {
		return nil, err
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL = &url.URL{Scheme: target.Scheme, Host: target.Host, Path: target.Path}
			r.Out.Host = target.Host
			r.Out.Header.Del("Origin")
			r.Out.Header.Set("Authorization", localrpc.FormatAuthHeader(c.Token))
		},
		Transport: NewBaseTransport(),
		ErrorLog:  log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, err.Error(), http.StatusBadGateway)
		},
	}
	mux := http.NewServeMux()
	mux.Handle("POST "+CollectorPath, proxy)
	return mux, nil
}
