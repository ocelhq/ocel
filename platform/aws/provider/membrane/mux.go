package membrane

import (
	"net/http"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/proto/buckets/v1/bucketsv1connect"
	"github.com/ocelhq/ocel/platform/aws/provider/channelauth"
)

func NewMux(token string, svc bucketsv1connect.BucketServiceHandler) *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := bucketsv1connect.NewBucketServiceHandler(
		svc,
		connect.WithInterceptors(channelauth.Interceptor(token)),
	)
	mux.Handle(path, handler)
	return mux
}
