package provider_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
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

func TestTheRoutersAnEdgePairsWithAreListedOnceEachInPairingOrder(t *testing.T) {
	t.Parallel()

	facts := provider.Facts{Pairings: []provider.Pairing{
		{Router: "origin", Computes: []provider.Compute{provider.ComputeContainer}},
		{Edge: "front", Router: "functions", Computes: []provider.Compute{provider.ComputeServerless}},
		{Edge: "front", Router: "containers", Computes: []provider.Compute{provider.ComputeContainer}},
		{Edge: "shared", Router: "both", Computes: []provider.Compute{provider.ComputeServerless}},
		{Edge: "shared", Router: "both", Computes: []provider.Compute{provider.ComputeContainer}},
	}}
	if got := facts.ListPairedRouters("front"); !slices.Equal(got, []router.Kind{"functions", "containers"}) {
		t.Errorf("ListPairedRouters(front) = %v, want [functions containers]", got)
	}
	if got := facts.ListPairedRouters("shared"); !slices.Equal(got, []router.Kind{"both"}) {
		t.Errorf("ListPairedRouters(shared) = %v, want the one router both computes pair with, once", got)
	}
	if got := facts.ListPairedRouters(edge.None); !slices.Equal(got, []router.Kind{"origin"}) {
		t.Errorf("ListPairedRouters(no edge) = %v, want the router an app with no edge in front routes through", got)
	}
	if got := facts.ListPairedRouters("other"); len(got) != 0 {
		t.Errorf("ListPairedRouters(other) = %v, want none for an edge nothing pairs", got)
	}
}

func TestAnEdgesOwnRouterIsTheOneItPairsWithoutForwardingWhateverTheOrderOfPairings(t *testing.T) {
	t.Parallel()

	forwardedFirst := provider.Facts{Pairings: []provider.Pairing{
		{Edge: "front", Router: "containers", Computes: []provider.Compute{provider.ComputeContainer}, Forwarded: true},
		{Edge: "front", Router: "functions", Computes: []provider.Compute{provider.ComputeServerless}},
	}}
	if got, found := forwardedFirst.FindEdgeRouter("front"); !found || got != "functions" {
		t.Errorf("FindEdgeRouter(front) = %q, %v, want functions: the edge forwards the containers router's hostnames and runs its own routing for the functions router's", got, found)
	}
	if !forwardedFirst.IsForwarded("front", "containers") || forwardedFirst.IsForwarded("front", "functions") {
		t.Error("IsForwarded names the wrong pairing forwarded")
	}
	if got, found := forwardedFirst.FindEdgeRouter("other"); found {
		t.Errorf("FindEdgeRouter(other) = %q, want none for an edge nothing pairs", got)
	}
}
