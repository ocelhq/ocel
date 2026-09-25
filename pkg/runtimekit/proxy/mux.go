package proxy

import (
	"net/http"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
)

func NewMux(token string, svc bucketv1connect.BucketServiceHandler) *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := bucketv1connect.NewBucketServiceHandler(
		svc,
		connect.WithInterceptors(&authenticator{token: token}, validate.NewInterceptor()),
	)
	mux.Handle(path, handler)
	return mux
}
