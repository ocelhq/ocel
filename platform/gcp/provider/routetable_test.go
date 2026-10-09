package gcp

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
)

const routeTableKey = "prod/shop/web/r1a2b3c4d/route-table/ab.json"

func routedSpec(key string) provider.StackSpec {
	return provider.StackSpec{
		Ref:  provider.StackRef{Project: "shop", Tier: environment.TierProduction},
		Kind: provider.StackApp,
		Edge: codeRunningFront{kind: cloudflareKind, runsCode: true},
		App: &provider.AppSpec{App: "web", Compute: provider.ComputeServerless, EdgeRouteTable: &provider.EdgeRouteTable{
			Location: router.RouteTableLocation{Format: edge.RouteTableNext, Key: key},
			Table:    []byte(`{"routes":[{"source":"^/(?<slug>[^/]+)$"}]}`),
		}},
	}
}

func TestADeployRoutedByAnEdgeRouteTablePutsItAtItsKeyInTheAdoptedCacheStoreAndAnswersTheKey(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	store, endpoint := serveCacheStore(t)
	adoptCacheStoreAt(t, h, endpoint)
	spec := routedSpec(routeTableKey)
	wrapped := stacks{Stacks: recordingStacks{order: &orderLog{}}, p: programming(h)}

	result, err := wrapped.Provision(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}

	if result.RouteTableKey != routeTableKey {
		t.Errorf("RouteTableKey = %q, want %q", result.RouteTableKey, routeTableKey)
	}
	if got := store.keys(); len(got) != 1 || got[0] != routeTableKey {
		t.Fatalf("the cache store keeps %v, want only %q", got, routeTableKey)
	}
	put := store.objects[routeTableKey]
	if put.body != string(spec.App.EdgeRouteTable.Table) {
		t.Errorf("uploaded body = %q, want the table verbatim", put.body)
	}
	if put.contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", put.contentType)
	}
	if !strings.Contains(put.authorization, "Credential=r2-key/") {
		t.Errorf("authorization = %q, want a request signed with the adopted cache store's key", put.authorization)
	}
}

func TestADeployRoutedByAnEdgeRouteTableWithNoAdoptedCacheStoreIsNotReadyAndDeploysNothing(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	if err := h.adopt([]edge.Offer{storeOffer("c1"), writerOffer("c2"), certificateOffer()}, nil); err != nil {
		t.Fatal(err)
	}
	order := &orderLog{}
	wrapped := stacks{Stacks: recordingStacks{order: order}, p: programming(h)}

	_, err := wrapped.Provision(t.Context(), routedSpec(routeTableKey), nil)

	if refusalCode(err) != refusal.CodeNotReady || !strings.Contains(err.Error(), provider.BootstrapCommand(environment.TierProduction)) {
		t.Errorf("Provision = %v, want a not-ready refusal naming %q", err, provider.BootstrapCommand(environment.TierProduction))
	}
	if len(order.steps) != 0 {
		t.Errorf("steps = %v, want nothing deployed", order.steps)
	}
}

func TestADeployRoutedByNoEdgeRouteTableAnswersNoKeyAndReachesNoCacheStore(t *testing.T) {
	t.Parallel()
	h := newOffersHarness(t)
	store, endpoint := serveCacheStore(t)
	adoptCacheStoreAt(t, h, endpoint)
	spec := routedSpec(routeTableKey)
	spec.App.EdgeRouteTable = nil
	wrapped := stacks{Stacks: recordingStacks{order: &orderLog{}}, p: programming(h)}

	result, err := wrapped.Provision(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}
	if result.RouteTableKey != "" || len(store.keys()) != 0 {
		t.Errorf("RouteTableKey = %q and the cache store keeps %v, want neither", result.RouteTableKey, store.keys())
	}
}
