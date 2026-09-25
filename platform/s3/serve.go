package s3

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/runtimekit/bindingproxy"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
)

func ServeBound(records Records, app string) (bindingproxy.Served, error) {
	if records == nil || !slices.ContainsFunc(records.Bindings(), func(l live.Binding) bool { return naming.Proxied(l.Type) }) {
		return bindingproxy.Served{}, nil
	}
	return bindingproxy.Serve(RouteRecords(nil, records, HTTPPoster{App: app}))
}
