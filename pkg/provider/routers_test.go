package provider_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func TestAnAppRoutesThroughTheRouterItsEdgeAndComputePairWith(t *testing.T) {
	t.Parallel()

	facts := provider.Facts{Pairings: []provider.Pairing{
		{Edge: "front", Router: "functions", Computes: []provider.Compute{provider.ComputeServerless}},
		{Edge: "front", Router: "containers", Computes: []provider.Compute{provider.ComputeContainer}},
	}}
	for compute, want := range map[provider.Compute]router.Kind{
		provider.ComputeServerless: "functions",
		provider.ComputeContainer:  "containers",
	} {
		got, found := facts.PairedRouter("front", compute)
		if !found || got != want {
			t.Errorf("PairedRouter(front, %s) = %q, %v, want %q", compute, got, found, want)
		}
	}
	if got, found := facts.PairedRouter("other", provider.ComputeServerless); found {
		t.Errorf("PairedRouter(other, serverless) = %q, want no router for an edge nothing pairs", got)
	}
}
