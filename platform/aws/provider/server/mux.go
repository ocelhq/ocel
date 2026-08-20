package server

import (
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
	"github.com/ocelhq/ocel/pkg/proto/env/v1/envv1connect"
	"github.com/ocelhq/ocel/platform/aws/provider/channelauth"
	"github.com/ocelhq/ocel/platform/aws/provider/tracecontext"
)

func NewMux(token string) *http.ServeMux {
	return newMux(&Server{}, token)
}

func newMux(deployments *Server, token string) *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(channelauth.Interceptor(token), tracecontext.Interceptor())

	path, handler := deploymentsv1connect.NewDeploymentServiceHandler(deployments, interceptors)
	mux.Handle(path, handler)

	path, handler = envv1connect.NewEnvVarsServiceHandler(&VarsServer{config: &deployments.config}, interceptors)
	mux.Handle(path, handler)
	return mux
}
