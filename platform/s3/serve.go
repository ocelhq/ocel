package s3

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	"github.com/ocelhq/ocel/pkg/runtimekit/proxy"
)

func ServeBound(records Records, app string) (proxy.Served, error) {
	if records == nil || !slices.ContainsFunc(records.Bindings(), func(l live.Binding) bool { return naming.Proxied(l.Type) }) {
		return proxy.Served{}, nil
	}
	return proxy.Serve(RouteRecords(nil, records, HTTPPoster{App: app}))
}
