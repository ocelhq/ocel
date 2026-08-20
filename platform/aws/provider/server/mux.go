package server

import (
	"net/http"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
	"github.com/ocelhq/ocel/pkg/proto/envvars/v1/envvarsv1connect"
	"github.com/ocelhq/ocel/platform/aws/provider/channelauth"
	"github.com/ocelhq/ocel/platform/aws/provider/tracecontext"
)

func NewMux(token string) *http.ServeMux {
	return newMux(&Server{}, token)
}

func newMux(deployments *Server, token string) *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(channelauth.Interceptor(token), tracecontext.Interceptor(), validate.NewInterceptor())

	path, handler := deploymentsv1connect.NewProviderServiceHandler(deployments, interceptors)
	mux.Handle(path, handler)

	path, handler = envvarsv1connect.NewEnvVarsServiceHandler(&VarsServer{stores: &deployments.stores, config: &deployments.config}, interceptors)
	mux.Handle(path, handler)
	return mux
}
