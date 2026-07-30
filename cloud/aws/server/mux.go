package server

import (
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
	"github.com/ocelhq/ocel/pkg/proto/env/v1/envv1connect"
)

// NewMux wires the provider's services behind an interceptor that enforces
// token as the per-session token every call must present (see
// newAuthInterceptor). EnvVarsService rides the same channel and the same
// handshake as DeploymentService: the CLI has no cloud SDK dependency, so
// every read and write of a variable goes through this process.
func NewMux(token string) *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(newAuthInterceptor(token))

	path, handler := deploymentsv1connect.NewDeploymentServiceHandler(&Server{}, interceptors)
	mux.Handle(path, handler)

	path, handler = envv1connect.NewEnvVarsServiceHandler(&VarsServer{}, interceptors)
	mux.Handle(path, handler)
	return mux
}
