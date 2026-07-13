package server

import (
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
)

// NewMux wires DeploymentService behind an interceptor that enforces token as
// the per-session token every call must present (see newAuthInterceptor).
func NewMux(token string) *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := deploymentsv1connect.NewDeploymentServiceHandler(
		&Server{},
		connect.WithInterceptors(newAuthInterceptor(token)),
	)
	mux.Handle(path, handler)
	return mux
}
