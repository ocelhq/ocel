package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
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
	server := &googletest.Run{}
	spec := routedSpec(routeTableKey)
	wrapped := stacks{Stacks: recordingStacks{order: &orderLog{}}, p: providerServing(server, t)}

	result, err := wrapped.Provision(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}

	if result.RouteTableKey != routeTableKey {
		t.Errorf("RouteTableKey = %q, want %q", result.RouteTableKey, routeTableKey)
	}
	uploads := server.Stored()
	if names := uploadNames(uploads); len(names) != 1 {
		t.Fatalf("the deploy uploaded %v, want only the route table", names)
	}
	put := uploadNamed(t, uploads, provider.StoreAssets+"/"+routeTableKey)
	if string(put.Body) != string(spec.App.EdgeRouteTable.Table) {
		t.Errorf("uploaded body = %q, want the table verbatim", put.Body)
	}
	if put.ContentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", put.ContentType)
	}
	if put.IfGenerationMatch != "0" {
		t.Errorf("ifGenerationMatch = %q, want 0: the key names the table's digest, so a table already there is this one", put.IfGenerationMatch)
	}
}

func TestADeployRoutedByAnEdgeRouteTableWithNoEdgeIsRefusedAndUploadsNothing(t *testing.T) {
	server := &googletest.Run{}
	spec := routedSpec(routeTableKey)
	spec.Edge = nil
	order := &orderLog{}
	wrapped := stacks{Stacks: recordingStacks{order: order}, p: providerServing(server, t)}

	_, err := wrapped.Provision(t.Context(), spec, nil)

	if refusalCode(err) != refusal.CodeInvalid {
		t.Errorf("Provision = %v, want an invalid refusal", err)
	}
	if len(order.steps) != 0 || len(server.Stored()) != 0 {
		t.Errorf("steps = %v and uploads = %v, want nothing deployed", order.steps, uploadNames(server.Stored()))
	}
}

func TestADeployRoutedByNoEdgeRouteTableAnswersNoKeyAndUploadsNothing(t *testing.T) {
	server := &googletest.Run{}
	spec := routedSpec(routeTableKey)
	spec.App.EdgeRouteTable = nil
	wrapped := stacks{Stacks: recordingStacks{order: &orderLog{}}, p: providerServing(server, t)}

	result, err := wrapped.Provision(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}
	if result.RouteTableKey != "" || len(server.Stored()) != 0 {
		t.Errorf("RouteTableKey = %q and the deploy uploaded %v, want neither", result.RouteTableKey, uploadNames(server.Stored()))
	}
}
