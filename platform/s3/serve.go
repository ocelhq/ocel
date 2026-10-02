package s3

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
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
	if records == nil || !slices.ContainsFunc(records.Bindings(), func(l live.Binding) bool { return naming.Proxied(l.Type) }) {
		return nil
	}
	return NewDispatch(nil, records, HTTPPoster{App: app})
}
