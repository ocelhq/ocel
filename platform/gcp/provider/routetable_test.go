package gcp

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/runtime/baked"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

const routeTableKey = "route-tables/shop/prod/web/r1a2b3c4d/ab.json"

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
	put := uploadNamed(t, uploads, routeTableKey)
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

func plantEdgeBundle(t *testing.T, spec provider.StackSpec) {
	t.Helper()
	bundle := filepath.Join(buildoutput.AppRoot(spec.App.OutputRoot, "web"), filepath.FromSlash(edge.AppBundleFile))
	if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundle, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestADeployTheEdgesCodeDispatchesPutsItsEdgeBundleAndSealedValuesWhereTheWorkerLoadsThem(t *testing.T) {
	server := &googletest.Run{}
	spec := routedSpec(routeTableKey)
	spec.App.OutputRoot = t.TempDir()
	spec.App.StorageCoordinate = naming.Coordinate{Project: "shop", Env: "prod", App: "web", Release: naming.NewReleaseToken("b1", "")}
	spec.App.Values = provider.AppValues{Plain: map[string]string{"SITE": "shop"}, Sensitive: map[string]string{"API_KEY": "k3y"}}
	plantEdgeBundle(t, spec)
	wrapped := stacks{Stacks: recordingStacks{order: &orderLog{}}, p: providerServing(server, t)}

	result, err := wrapped.Provision(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}

	bundleKey := spec.App.StorageCoordinate.BundleKey("bundle.json")
	if result.EdgeBundleKey != bundleKey {
		t.Errorf("EdgeBundleKey = %q, want %q: the worker runs the app's middleware and edge routes from it", result.EdgeBundleKey, bundleKey)
	}
	if put := uploadNamed(t, server.Stored(), bundleKey); string(put.Body) != `{"version":1}` {
		t.Errorf("uploaded bundle = %q, want the build's own", put.Body)
	}
	key, err := base64.StdEncoding.DecodeString(result.Envelope)
	if err != nil {
		t.Fatalf("Envelope = %q: %v", result.Envelope, err)
	}
	sealed := uploadNamed(t, server.Stored(), spec.App.StorageCoordinate.BundleKey(baked.EdgeFile))
	values, err := baked.Open(key, sealed.Body)
	if err != nil || values["API_KEY"] != "k3y" {
		t.Errorf("the sealed values open to %v, %v, want the app's sensitive values for its edge code", values, err)
	}
}

func TestADeployWhoseEdgeDispatchesItNotUploadsNoEdgeBundle(t *testing.T) {
	server := &googletest.Run{}
	spec := routedSpec(routeTableKey)
	spec.App.EdgeRouteTable = nil
	spec.App.OutputRoot = t.TempDir()
	plantEdgeBundle(t, spec)
	wrapped := stacks{Stacks: recordingStacks{order: &orderLog{}}, p: providerServing(server, t)}

	result, err := wrapped.Provision(t.Context(), spec, nil)
	if err != nil {
		t.Fatalf("Provision = %v", err)
	}
	if result.EdgeBundleKey != "" || len(server.Stored()) != 0 {
		t.Errorf("EdgeBundleKey = %q and uploads %v, want neither where no edge runs the app's code", result.EdgeBundleKey, uploadNames(server.Stored()))
	}
}
