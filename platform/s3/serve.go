package s3

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/pkg/runtime/live"
)

func ServeBound(records Records, app string) (bindingproxy.Served, error) {
	buckets := NewBoundDispatch(records, app)
	if buckets == nil {
		return bindingproxy.Served{}, nil
	}
	return bindingproxy.Serve(bindingproxy.Services{Buckets: buckets})
}

func NewBoundDispatch(records Records, app string) bucketv1connect.BucketServiceHandler {
	if records == nil || !slices.ContainsFunc(records.Bindings(), func(l live.Binding) bool { return l.Type == bindingsv1.BindingType_BINDING_TYPE_BUCKET }) {
		return nil
	}
	return NewDispatch(nil, records, HTTPPoster{App: app})
}
