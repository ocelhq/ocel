package assets

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/provider"
)

func TestANextAppAnEdgeRoutesUploadsItsStaticFiles(t *testing.T) {
	t.Parallel()

	for name, app := range map[string]*provider.AppSpec{
		"routed at the origin": {Routing: &provider.RoutingSpec{}},
		"routed at the edge":   {EdgeRouteTable: &provider.EdgeRouteTable{}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app.Framework = buildoutput.FrameworkNext
			app.Compute = provider.ComputeServerless

			if !servesStaticFiles(provider.StackSpec{App: app}) {
				t.Error("the deploy uploads no static files, and the image carries none, so no edge can serve them")
			}
		})
	}
}
