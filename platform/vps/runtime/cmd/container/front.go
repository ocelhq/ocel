package main

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/platform/realtime/gateway"
	boxlive "github.com/ocelhq/ocel/platform/vps/provider/live"
)

func newFront(manifest boxlive.Manifest, opts originguard.Options) (http.Handler, error) {
	app := originguard.Handler(opts)
	if manifest.RealtimePublishURL == "" {
		return app, nil
	}
	target, err := findRealtimeGateway(manifest.RealtimePublishURL)
	if err != nil {
		return nil, err
	}
	socket := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			r.Out.URL.Path, r.Out.URL.RawPath = gateway.SocketPath, ""
			r.Out.Header.Del("Origin")
			r.Out.Header.Del(originguard.OriginSecretHeader)
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "the realtime gateway did not answer", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != boxlive.RealtimeSocketPath {
			app.ServeHTTP(w, r)
			return
		}
		if !opts.Guard.Admits(r) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		socket.ServeHTTP(w, r)
	}), nil
}

func findRealtimeGateway(publishURL string) (*url.URL, error) {
	published, err := url.Parse(publishURL)
	if err != nil || published.Scheme == "" || published.Host == "" {
		return nil, fmt.Errorf("this container serves realtime at %s, but its gateway's publish address %q names no gateway to reach",
			boxlive.RealtimeSocketPath, publishURL)
	}
	return &url.URL{Scheme: published.Scheme, Host: published.Host}, nil
}
