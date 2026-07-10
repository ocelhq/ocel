package main

import (
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/proto/provider/v1/providerv1connect"
)

// newMux wires ProviderService behind an interceptor that enforces token as
// the per-session token every call must present (see newAuthInterceptor).
func newMux(token string) *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := providerv1connect.NewProviderServiceHandler(
		&Server{},
		connect.WithInterceptors(newAuthInterceptor(token)),
	)
	mux.Handle(path, handler)
	return mux
}
