package membrane

import (
	"net/http"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/proto/app/blob/v1/blobv1connect"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/channelauth"
)

var served = map[linksv1.LinkType]bool{
	linksv1.LinkType_LINK_TYPE_BUCKET: true,
}

func Serves(t linksv1.LinkType) bool {
	return served[t]
}

func NewMux(token string, svc blobv1connect.BucketServiceHandler) *http.ServeMux {
	mux := http.NewServeMux()
	path, handler := blobv1connect.NewBucketServiceHandler(
		svc,
		connect.WithInterceptors(channelauth.Interceptor(token), validate.NewInterceptor()),
	)
	mux.Handle(path, handler)
	return mux
}
