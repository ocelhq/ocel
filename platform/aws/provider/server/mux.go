package server

import (
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/proto/deployments/v1/deploymentsv1connect"
	"github.com/ocelhq/ocel/pkg/proto/env/v1/envv1connect"
	"github.com/ocelhq/ocel/platform/aws/provider/channelauth"
)

func NewMux(token string) *http.ServeMux {
	mux := http.NewServeMux()
	interceptors := connect.WithInterceptors(channelauth.Interceptor(token))

	path, handler := deploymentsv1connect.NewDeploymentServiceHandler(&Server{}, interceptors)
	mux.Handle(path, handler)

	path, handler = envv1connect.NewEnvVarsServiceHandler(&VarsServer{}, interceptors)
	mux.Handle(path, handler)
	return mux
}
