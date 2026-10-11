package gcp

import (
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

func TestADeployRoutedByAnEdgeRouteTablePutsItAtItsKeyInTheTiersAssetsStoreAndAnswersTheKey(t *testing.T) {
	server := &runServer{}
	spec := routedSpec(routeTableKey)
	wrapped := stacks{Stacks: recordingStacks{order: &orderLog{}}, p: server.open(t)}

	result, err := wrapped.Provision(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}

	if result.RouteTableKey != routeTableKey {
		t.Errorf("RouteTableKey = %q, want %q", result.RouteTableKey, routeTableKey)
	}
	uploads := server.stored()
	if names := uploadNames(uploads); len(names) != 1 {
		t.Fatalf("the deploy uploaded %v, want only the route table", names)
	}
	put := uploadNamed(t, uploads, provider.StoreAssets+"/"+routeTableKey)
	if string(put.body) != string(spec.App.EdgeRouteTable.Table) {
		t.Errorf("uploaded body = %q, want the table verbatim", put.body)
	}
	if put.contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", put.contentType)
	}
	if put.ifGenerationMatch != "0" {
		t.Errorf("ifGenerationMatch = %q, want 0: the key names the table's digest, so a table already there is this one", put.ifGenerationMatch)
	}
}

func TestADeployRoutedByAnEdgeRouteTableWithNoEdgeIsRefusedAndUploadsNothing(t *testing.T) {
	server := &runServer{}
	spec := routedSpec(routeTableKey)
	spec.Edge = nil
	order := &orderLog{}
	wrapped := stacks{Stacks: recordingStacks{order: order}, p: server.open(t)}

	_, err := wrapped.Provision(t.Context(), spec, nil)

	if refusalCode(err) != refusal.CodeInvalid {
		t.Errorf("Provision = %v, want an invalid refusal", err)
	}
	if len(order.steps) != 0 || len(server.stored()) != 0 {
		t.Errorf("steps = %v and uploads = %v, want nothing deployed", order.steps, uploadNames(server.stored()))
	}
}

func TestADeployRoutedByNoEdgeRouteTableAnswersNoKeyAndUploadsNothing(t *testing.T) {
	server := &runServer{}
	spec := routedSpec(routeTableKey)
	spec.App.EdgeRouteTable = nil
	wrapped := stacks{Stacks: recordingStacks{order: &orderLog{}}, p: server.open(t)}

	result, err := wrapped.Provision(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}
	if result.RouteTableKey != "" || len(server.stored()) != 0 {
		t.Errorf("RouteTableKey = %q and the deploy uploaded %v, want neither", result.RouteTableKey, uploadNames(server.stored()))
	}
}
