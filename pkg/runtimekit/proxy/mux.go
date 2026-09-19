package proxy

import (
	"net/http"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

var served = map[bindingsv1.BindingType]bool{
	bindingsv1.BindingType_BINDING_TYPE_BUCKET: true,
}

func Serves(t bindingsv1.BindingType) bool {
	return served[t]
}

func NewMux(token string, svc bucketv1connect.BucketServiceHandler) *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := bucketv1connect.NewBucketServiceHandler(
		svc,
		connect.WithInterceptors(&authenticator{token: token}, validate.NewInterceptor()),
	)
	mux.Handle(path, handler)
	return mux
}
